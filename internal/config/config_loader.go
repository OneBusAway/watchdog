package config

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// ValidateConfigFlags ensures that only one configuration source is specified:
// either a config file "--config-file", a remote config URL "--config-url".
//
// Returns an error if more than one input method is specified.
func ValidateConfigFlags(configFile, configURL *string) error {
	if *configFile == "" && *configURL == "" {
		return fmt.Errorf("no configuration provided, either --config-file or --config-url must be specified")
	}
	if (*configFile != "" && *configURL != "") || (*configFile != "" && len(flag.Args()) > 0) || (*configURL != "" && len(flag.Args()) > 0) {
		return fmt.Errorf("only one of --config-file or --config-url can be specified")
	}
	return nil
}

// refreshConfig starts a background goroutine that periodically fetches configuration
// from a remote URL and updates the application's list of OBA servers.
//
// It uses the provided HTTP client to make GET requests with optional basic auth,
// and on success, updates the application's configuration via `app.Config.UpdateConfig`.
//
// Errors during fetch or parse are logged and reported to Sentry, but the loop continues,
// ensuring resiliency in the presence of transient issues.
//
// The routine stops gracefully when the context is canceled.
func refreshConfig(ctx context.Context, client *http.Client, configURL, configAuthUser, configAuthPass string, cfg *Config, logger *slog.Logger, interval time.Duration, maxRetries int) {
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping config refresh routine")
			return
		default:
			newServers, err := loadConfigFromURL(ctx, client, configURL, configAuthUser, configAuthPass, maxRetries)
			if err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:  utils.MakeMap("config_url", configURL),
					Level: sentry.LevelError,
				})
				logger.Error("Failed to refresh remote config", "error", err)
			} else {
				cfg.UpdateConfig(newServers)
				logger.Info("Successfully refreshed server configuration")
			}
			time.Sleep(interval)
		}
	}
}

// LoadConfigFromFile reads a JSON configuration file from disk and unmarshals it
// into a list of OBA server configurations (`[]models.ObaServer`).
//
// On error, it reports issues to Sentry and returns a descriptive error.
//
// This function is used when the application is configured to load its server list
// from a static file using the --config-file flag.
func loadConfigFromFile(filePath string) ([]models.ObaServer, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}

// LoadConfigFromURL fetches a JSON configuration from a remote HTTP(S) endpoint,
// using the provided client and optional basic authentication.
//
// It validates the response status, reads the body, and unmarshals the configuration
// into a slice of `models.ObaServer`.
//
// Errors are logged and reported to Sentry for observability.
func loadConfigFromURL(ctx context.Context, client *http.Client, url, authUser, authPass string, maxRetries int) ([]models.ObaServer, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	if authUser != "" && authPass != "" {
		req.SetBasicAuth(authUser, authPass)
	}

	resp, err := DoWithBackoff(ctx, client, req, maxRetries)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to fetch remote config: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("remote config returned status: %d", resp.StatusCode)
		report.ReportErrorWithSentryOptions(statusErr, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, statusErr
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to read remote config: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}
