package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/server"
	"watchdog.onebusaway.org/internal/utils"
)

// Declare a string containing the application version number. Later in the book we'll
// generate this automatically at build time, but for now we'll just store the version
// number as a hard-coded global constant.
const version = "1.0.0"

// Define an application struct to hold the dependencies for our HTTP handlers, helpers,
// and middleware. At the moment this only contains a copy of the config struct and a
// logger, but it will grow to include a lot more as our build progresses.

type application struct {
	config   server.Config
	logger   *slog.Logger
	reporter *report.Reporter
	mu       sync.RWMutex
}

func main() {
	var cfg server.Config

	flag.IntVar(&cfg.Port, "port", 4000, "API server port")
	flag.StringVar(&cfg.Env, "env", "development", "Environment (development|staging|production)")

	var (
		configFile = flag.String("config-file", "", "Path to a local JSON configuration file")
		configURL  = flag.String("config-url", "", "URL to a remote JSON configuration file")
	)

	flag.Parse()

	configAuthUser := os.Getenv("CONFIG_AUTH_USER")
	configAuthPass := os.Getenv("CONFIG_AUTH_PASS")

	var err error

	if err = validateConfigFlags(configFile, configURL); err != nil {
		fmt.Println("Error:", err)
		flag.Usage()
		os.Exit(1)
	}

	report.SetupSentry()
	defer report.FlushSentry()

	reporter := report.NewReporter(cfg.Env, version)
	reporter.ConfigureScope()

	var servers []models.ObaServer

	if *configFile != "" {
		servers, err = loadConfigFromFile(*configFile, reporter)
	} else if *configURL != "" {
		servers, err = loadConfigFromURL(*configURL, configAuthUser, configAuthPass, reporter)
	} else {
		fmt.Println("Error: No configuration provided. Use --config-file or --config-url.")
		flag.Usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if len(servers) == 0 {
		fmt.Println("Error: No servers found in configuration.")
		os.Exit(1)
	}

	cfg.Servers = servers

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cacheDir := "cache"
	if err = createCacheDirectory(cacheDir, logger, reporter); err != nil {
		logger.Error("Failed to create cache directory", "error", err)
		os.Exit(1)
	}

	// Download GTFS bundles for all servers on startup
	downloadGTFSBundles(servers, cacheDir, logger, reporter)

	app := &application{
		config:   cfg,
		logger:   logger,
		reporter: reporter,
	}

	app.startMetricsCollection()

	// Cron job to download GTFS bundles for all servers every 24 hours
	go refreshGTFSBundles(servers, cacheDir, logger, 24*time.Hour, reporter)

	// If a remote URL is specified, refresh the configuration every minute
	if *configURL != "" {
		go refreshConfig(*configURL, configAuthUser, configAuthPass, app, logger, time.Minute, reporter)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      app.routes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	logger.Info("starting server", "addr", srv.Addr, "env", cfg.Env)
	err = srv.ListenAndServe()
	reporter.ReportError(err, sentry.LevelFatal)
	report.FlushSentry()
	logger.Error(err.Error())
	os.Exit(1)
}

// validateConfigFlags checks that only one of --config-file, --config-url, or an additional argument is provided.
func validateConfigFlags(configFile, configURL *string) error {
	if (*configFile != "" && *configURL != "") || (*configFile != "" && len(flag.Args()) > 0) || (*configURL != "" && len(flag.Args()) > 0) {
		return fmt.Errorf("only one of --config-file or --config-url can be specified")
	}
	return nil
}

// createCacheDirectory ensures the cache directory exists, creating it if necessary.
func createCacheDirectory(cacheDir string, logger *slog.Logger, reporter *report.Reporter) error {
	stat, err := os.Stat(cacheDir)

	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(cacheDir, os.ModePerm); err != nil {
				reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Level: sentry.LevelError,
					ExtraContext: map[string]interface{}{
						"cache_dir": cacheDir,
					},
				})
				return err
			}
			return nil
		}
		return err

	}
	if !stat.IsDir() {
		err := fmt.Errorf("%s is not a directory", cacheDir)
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Level: sentry.LevelError,
			ExtraContext: map[string]interface{}{
				"cache_dir": cacheDir,
			},
		})
		return err
	}
	return nil
}

// downloadGTFSBundles downloads GTFS bundles for each server and caches them locally.
func downloadGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger, reporter *report.Reporter) {
	for _, server := range servers {
		hash := sha1.Sum([]byte(server.GtfsUrl))
		hashStr := hex.EncodeToString(hash[:])
		cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", server.ID, hashStr))

		_, err := utils.DownloadGTFSBundle(server.GtfsUrl, cacheDir, server.ID, hashStr)
		if err != nil {
			reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"server_id": fmt.Sprintf("%d", server.ID),
				},
				ExtraContext: map[string]interface{}{
					"gtfs_url": server.GtfsUrl,
				},
				Level: sentry.LevelError,
			})
			logger.Error("Failed to download GTFS bundle", "server_id", server.ID, "error", err)
		} else {
			logger.Info("Successfully downloaded GTFS bundle", "server_id", server.ID, "path", cachePath)
		}
	}
}

// refreshGTFSBundles periodically downloads GTFS bundles at the specified interval.
func refreshGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger, interval time.Duration, reporter *report.Reporter) {
	for {
		time.Sleep(interval)
		downloadGTFSBundles(servers, cacheDir, logger, reporter)
	}
}

// refreshConfig periodically fetches remote config and updates the application servers.
func refreshConfig(configURL, configAuthUser, configAuthPass string, app *application, logger *slog.Logger, interval time.Duration, reporter *report.Reporter) {
	for {
		time.Sleep(interval)
		newServers, err := loadConfigFromURL(configURL, configAuthUser, configAuthPass, reporter)
		if err != nil {
			reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"config_url": configURL,
				},
				Level: sentry.LevelError,
			})
			logger.Error("Failed to refresh remote config", "error", err)
			continue
		}

		app.updateConfig(newServers)
		logger.Info("Successfully refreshed server configuration")
	}
}

// updateConfig safely updates the application's server configuration.
func (app *application) updateConfig(newServers []models.ObaServer) {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.config.Servers = newServers
}

func loadConfigFromFile(filePath string, reporter *report.Reporter) ([]models.ObaServer, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"file_path": filePath,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"file_path": filePath,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}

func loadConfigFromURL(url, authUser, authPass string, reporter *report.Reporter) ([]models.ObaServer, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"config_url": url,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	if authUser != "" && authPass != "" {
		req.SetBasicAuth(authUser, authPass)
	}

	resp, err := client.Do(req)
	if err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"config_url": url,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to fetch remote config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("remote config returned status: %d", resp.StatusCode)
		reporter.ReportErrorWithSentryOptions(statusErr, report.SentryReportOptions{
			Tags: map[string]string{
				"config_url": url,
			},
			Level: sentry.LevelError,
		})
		return nil, statusErr
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"config_url": url,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to read remote config: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"config_url": url,
			},
			Level: sentry.LevelError,
		})
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}
