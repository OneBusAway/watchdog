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
	"path/filepath"
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

// refreshConfig starts a background goroutine that periodically fetches
// configuration from a remote URL and updates the application's list of OBA servers.
//
// The fetch process is resilient:
//   - It uses `loadConfigFromURL`, which applies exponential backoff retries
//     (up to `maxRetries`) when transient network or parsing errors occur.
//   - On success, the application's configuration is updated via `cfg.UpdateConfig`.
//   - On failure, errors are logged and reported to Sentry, but the loop continues,
//     ensuring that the service keeps running even under repeated failures.
//
// The function runs in a loop, sleeping for the specified `interval` between
// refresh attempts, and terminates gracefully when the context is canceled.
//
// Parameters:
//   - ctx: Context for graceful cancellation of the refresh routine.
//   - client: HTTP client used to fetch the remote config.
//   - configURL: Remote URL to load configuration from.
//   - configAuthUser: Optional username for basic authentication.
//   - configAuthPass: Optional password for basic authentication.
//   - cfg: Pointer to the application Config object to update.
//   - logger: Logger for structured log output.
//   - interval: Time duration between consecutive refresh attempts.
//   - maxRetries: Maximum number of exponential backoff retries per fetch attempt.
//   - onUpdated: Callback invoked with the newly validated servers after a
//     successful config refresh. May be nil.
func refreshConfig(ctx context.Context, client *http.Client, configURL, configAuthUser, configAuthPass string, cfg *Config, logger *slog.Logger, interval time.Duration, maxRetries int, onUpdated func([]models.ObaServer)) {
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping config refresh routine")
			return
		default:
			newServers, err := loadConfigFromURL(ctx, client, configURL, configAuthUser, configAuthPass, maxRetries, logger)
			if err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:  utils.MakeMap("config_url", configURL),
					Level: sentry.LevelError,
				})
				logger.Error("Failed to refresh remote config", "error", err)
			} else if len(newServers) == 0 {
				// Do not apply an empty configuration. decodeServers drops
				// entries that fail validation rather than failing the whole
				// document, so a schema change that blanks a required field on
				// every entry — or an endpoint briefly serving "[]" during a
				// deploy — arrives here as an empty slice indistinguishable
				// from "the operator removed every server". Applying it stops
				// collection for the whole fleet, and downstream of that the
				// refresh callback would prune every store and retire every
				// series. Startup refuses to run with zero servers for the
				// same reason; a refresh must not do what startup rejects.
				err := fmt.Errorf("refreshed configuration from %s contained no valid servers; keeping the previous configuration", configURL)
				logger.Warn("Ignoring empty refreshed configuration",
					"config_url", configURL, "currently_configured", len(cfg.GetServers()))
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:  utils.MakeMap("config_url", configURL),
					Level: sentry.LevelWarning,
				})
			} else {
				cfg.UpdateConfig(newServers)
				logger.Info("Successfully refreshed server configuration")
				if onUpdated != nil {
					onUpdated(newServers)
				}
			}
			time.Sleep(interval)
		}
	}
}

// LoadConfigFromFile reads a JSON configuration file from disk and unmarshals it
// into a list of OBA server configurations (`[]models.ObaServer`).
//
// For security reasons, only files named `config.json` are allowed to be loaded.
// Without this restriction, a user could supply any file path on the machine
// (e.g., /etc/passwd), and the application would attempt to read it.
//
// On error, it reports issues to Sentry and returns a descriptive error.
//
// This function is used when the application is configured to load its server list
// from a static file using the --config-file flag.
func loadConfigFromFile(filePath string, logger *slog.Logger) ([]models.ObaServer, error) {
	if filepath.Base(filePath) != "config.json" {
		return nil, fmt.Errorf("invalid config file name: %s (only config.json is allowed)", filePath)
	}

	// #nosec G304 - file path validated by restricting to config.json
	data, err := os.ReadFile(filePath)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(data, &rawEntries); err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return decodeServers(rawEntries, logger), nil
}

// loadConfigFromURL fetches a JSON configuration from a remote HTTP(S) endpoint,
// using the provided client and optional basic authentication.
//
// It validates the response status, reads the body, and unmarshals the configuration
// into a slice of `models.ObaServer`.
//
// Requests are executed with exponential backoff using DoWithBackoff. This ensures
// that transient network errors (e.g., timeouts, connection failures) are retried
// with increasing delays, up to `maxRetries` attempts.
//
// Errors are logged and reported to Sentry for observability.
func loadConfigFromURL(ctx context.Context, client *http.Client, url, authUser, authPass string, maxRetries int, logger *slog.Logger) ([]models.ObaServer, error) {
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

	resp, err := utils.DoWithBackoff(ctx, client, req, maxRetries)
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

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(data, &rawEntries); err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return decodeServers(rawEntries, logger), nil
}
