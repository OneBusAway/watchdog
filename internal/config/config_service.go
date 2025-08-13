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
	Logger *slog.Logger
	Client *http.Client
	Config *Config
}

// NewConfigService creates a new ConfigService instance with the provided logger and HTTP client.
func NewConfigService(logger *slog.Logger, client *http.Client, config *Config) *ConfigService {
	return &ConfigService{
		Logger: logger,
		Client: client,
		Config: config,
	}
}

func (cs *ConfigService) RefreshConfig(ctx context.Context, url, authUser, authPass string, interval time.Duration) {
	refreshConfig(ctx, cs.Client, url, authUser, authPass, cs.Config, cs.Logger, interval)
}

// exported helper functions

// Load config from file and update Config.
func LoadConfigFromFile(filePath string) ([]models.ObaServer, error) {
	servers, err := loadConfigFromFile(filePath)
	if err != nil {
		err := fmt.Errorf("failed to load config from file %s: %w", filePath, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("file_path", filePath),
			Level: sentry.LevelError,
		})
		return nil ,err
	}
	return servers ,nil
}

// Load config from URL and update Config.
func LoadConfigFromURL(client *http.Client,url, authUser, authPass string) ([]models.ObaServer, error) {
	servers, err := loadConfigFromURL(client, url, authUser, authPass)
	if err != nil {
		err := fmt.Errorf("failed to load config from URL %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("config_url", url),
			Level: sentry.LevelError,
		})
		return nil, err
	}
	return servers,nil
}
