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
	"watchdog.onebusaway.org/internal/app"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// validateConfigFlags checks that only one of --config-file, --config-url, or an additional argument is provided.
func ValidateConfigFlags(configFile, configURL *string) error {
	if (*configFile != "" && *configURL != "") || (*configFile != "" && len(flag.Args()) > 0) || (*configURL != "" && len(flag.Args()) > 0) {
		return fmt.Errorf("only one of --config-file or --config-url can be specified")
	}
	return nil
}

// refreshConfig periodically fetches remote config and updates the application servers.
func RefreshConfig(ctx context.Context, configURL, configAuthUser, configAuthPass string, app *app.Application, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
		logger.Info("Stopping config refresh routine")
		return
		case <-ticker.C:
		newServers, err := LoadConfigFromURL(configURL, configAuthUser, configAuthPass)
		if err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags:  utils.MakeMap("config_url", configURL),
				Level: sentry.LevelError,
			})
			logger.Error("Failed to refresh remote config", "error", err)
			continue
		}

		app.Config.UpdateConfig(newServers)
		logger.Info("Successfully refreshed server configuration")
		}
	}
}

func LoadConfigFromFile(filePath string) ([]models.ObaServer, error) {
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

func LoadConfigFromURL(url, authUser, authPass string) ([]models.ObaServer, error) {
	client := &http.Client{}
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

	resp, err := client.Do(req)
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
