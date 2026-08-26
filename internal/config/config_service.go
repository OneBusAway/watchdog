package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// ConfigService holds dependencies and provides config operations.
type ConfigService struct {
	Logger       *slog.Logger
	Client       *http.Client
	Config       *Config
	BackoffStore *BackoffStore
}

// NewConfigService creates a new ConfigService instance with the provided logger and HTTP client.
func NewConfigService(logger *slog.Logger, client *http.Client, config *Config, backoffStore *BackoffStore) *ConfigService {
	return &ConfigService{
		Logger:       logger,
		Client:       client,
		Config:       config,
		BackoffStore: backoffStore,
	}
}

// RefreshConfig polls url in a loop, sleeping interval between attempts, and
// applies each successfully loaded, non-empty configuration -- an empty one is
// ignored rather than applied, see refreshConfig for why. onUpdated is invoked
// with the newly validated servers, inline on the polling loop, and must be
// safe for concurrent access with the collection goroutines.
func (cs *ConfigService) RefreshConfig(ctx context.Context, url, authUser, authPass string, interval time.Duration, maxRetries int, onUpdated func([]models.ObaServer)) {
	refreshConfig(ctx, cs.Client, url, authUser, authPass, cs.Config, cs.Logger, interval, maxRetries, onUpdated)
}

// exported helper functions

// Load config from file and update Config.
func LoadConfigFromFile(filePath string, logger *slog.Logger) ([]models.ObaServer, error) {
	servers, err := loadConfigFromFile(filePath, logger)
	if err != nil {
		err := fmt.Errorf("failed to load config from file %s: %w", filePath, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil, err
	}
	return servers, nil
}

// Load config from URL and update Config.
func LoadConfigFromURL(ctx context.Context, client *http.Client, url, authUser, authPass string, maxRetires int, logger *slog.Logger) ([]models.ObaServer, error) {
	servers, err := loadConfigFromURL(ctx, client, url, authUser, authPass, maxRetires, logger)
	if err != nil {
		err := fmt.Errorf("failed to load config from URL %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, err
	}
	return servers, nil
}
