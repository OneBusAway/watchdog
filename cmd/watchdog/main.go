package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/app"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/gtfs"
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

	if err = config.ValidateConfigFlags(configFile, configURL); err != nil {
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
		servers, err = config.LoadConfigFromFile(*configFile, reporter)
	} else if *configURL != "" {
		servers, err = config.LoadConfigFromURL(*configURL, configAuthUser, configAuthPass, reporter)
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
	if err = utils.CreateCacheDirectory(cacheDir, logger, reporter); err != nil {
		logger.Error("Failed to create cache directory", "error", err)
		os.Exit(1)
	}

	// Download GTFS bundles for all servers on startup
	gtfs.DownloadGTFSBundles(servers, cacheDir, logger, reporter)

	app := &app.Application{
		Config:   cfg,
		Logger:   logger,
		Reporter: reporter,
	}

	app.StartMetricsCollection()

	// Cron job to download GTFS bundles for all servers every 24 hours
	go gtfs.RefreshGTFSBundles(servers, cacheDir, logger, 24*time.Hour, reporter)

	// If a remote URL is specified, refresh the configuration every minute
	if *configURL != "" {
		go config.RefreshConfig(*configURL, configAuthUser, configAuthPass, app, logger, time.Minute, reporter)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      app.Routes(),
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
