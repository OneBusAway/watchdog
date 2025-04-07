package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
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
	config server.Config
	logger *slog.Logger
	mu     sync.RWMutex
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

	if (*configFile != "" && *configURL != "") || (*configFile != "" && len(flag.Args()) > 0) || (*configURL != "" && len(flag.Args()) > 0) {
		fmt.Println("Error: Only one of --config-file or --config-url can be specified.")
		flag.Usage()
		os.Exit(1)
	}

	var servers []models.ObaServer
	var err error

	if *configFile != "" {
		servers, err = loadConfigFromFile(*configFile)
	} else if *configURL != "" {
		servers, err = loadConfigFromURL(*configURL, configAuthUser, configAuthPass)
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

	setupSentry()

	cacheDir := "cache"

	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		if err := os.MkdirAll(cacheDir, os.ModePerm); err != nil {
			logger.Error("Failed to create cache directory", "error", err)
			os.Exit(1)
		}
	}

	// Download GTFS bundles for all servers on startup
	for _, server := range servers {
		hash := sha1.Sum([]byte(server.GtfsUrl))
		hashStr := hex.EncodeToString(hash[:])
		cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", server.ID, hashStr))

		_, err := utils.DownloadGTFSBundle(server.GtfsUrl, cacheDir, server.ID, hashStr)
		if err != nil {
			logger.Error("Failed to download GTFS bundle", "server_id", server.ID, "error", err)
		} else {
			logger.Info("Successfully downloaded GTFS bundle", "server_id", server.ID, "path", cachePath)
		}
	}

	app := &application{
		config: cfg,
		logger: logger,
	}

	app.startMetricsCollection()

	// Cron job to download GTFS bundles for all servers every 24 hours
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			for _, server := range servers {
				hash := sha1.Sum([]byte(server.GtfsUrl))
				hashStr := hex.EncodeToString(hash[:])
				cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", server.ID, hashStr))

				_, err := utils.DownloadGTFSBundle(server.GtfsUrl, cacheDir, server.ID, hashStr)
				if err != nil {
					logger.Error("Failed to download GTFS bundle", "server_id", server.ID, "error", err)
				} else {
					logger.Info("Successfully updated GTFS bundle", "server_id", server.ID, "path", cachePath)
				}
			}
		}
	}()

	// If a remote URL is specified, refresh the configuration every minute
	if *configURL != "" {
		go func() {
			for {
				time.Sleep(time.Minute)
				newServers, err := loadConfigFromURL(*configURL, configAuthUser, configAuthPass)
				if err != nil {
					logger.Error("Failed to refresh remote config", "error", err)
					continue
				}

				app.mu.Lock()
				cfg.Servers = newServers
				app.mu.Unlock()

				logger.Info("Successfully refreshed server configuration")
			}
		}()
	}

	// Use the httprouter instance returned by app.routes() as the server handler.
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
	sentry.CaptureException(err)
	logger.Error(err.Error())
	os.Exit(1)
}

func loadConfigFromFile(filePath string) ([]models.ObaServer, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}

func loadConfigFromURL(url, authUser, authPass string) ([]models.ObaServer, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	if authUser != "" && authPass != "" {
		req.SetBasicAuth(authUser, authPass)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote config returned status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read remote config: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}

func setupSentry() {

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              os.Getenv("SENTRY_DSN"),
		EnableTracing:    true,
		Debug:            true,
		TracesSampleRate: 1.0,
	}); err != nil {
		log.Fatalf("sentry.Init: %s", err)
	}

	defer sentry.Flush(2 * time.Second)

	sentry.CaptureMessage("Watchdog started")

}
