package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/app"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/server"
	"watchdog.onebusaway.org/internal/utils"
)

// Declare a string containing the application version number. Later in the book we'll
// generate this automatically at build time, but for now we'll just store the version
// number as a hard-coded global constant.
const version = "1.0.0"

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

	report.ConfigureScope(cfg.Env, version)

	var servers []models.ObaServer
	client := app.NewPooledClient()
	if *configFile != "" {
		servers, err = config.LoadConfigFromFile(*configFile)
	} else if *configURL != "" {
		servers, err = config.LoadConfigFromURL(client,*configURL, configAuthUser, configAuthPass)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cacheDir := "cache"
	if err = utils.CreateCacheDirectory(cacheDir, logger); err != nil {
		logger.Error("Failed to create cache directory", "error", err)
		os.Exit(1)
	}

	store := geo.NewBoundingBoxStore()

	// Download GTFS bundles for all servers on startup
	gtfs.DownloadGTFSBundles(servers, cacheDir, logger, store)

	vehicleLastSeen := metrics.NewVehicleLastSeen()

	realtimeStore := gtfs.NewRealtimeStore()

	app := &app.Application{
		Config:           cfg,
		Logger:           logger,
		Client: 					client,
		Version:          version,
		BoundingBoxStore: store,
		VehicleLastSeen:  vehicleLastSeen,
		RealtimeStore:    realtimeStore,
	}

	app.StartMetricsCollection(ctx)

	// Cron job to download GTFS bundles for all servers every 24 hours
	go gtfs.RefreshGTFSBundles(ctx, servers, cacheDir, logger, 24*time.Hour, store)

	// Cron job to delete the data of vehicles that has not sent updates for 1 hour
	go vehicleLastSeen.ClearRoutine(ctx, 15*time.Minute, time.Hour)

	// If a remote URL is specified, refresh the configuration every minute
	if *configURL != "" {
		go config.RefreshConfig(ctx ,app.Client,*configURL, configAuthUser, configAuthPass, app, logger, time.Minute)
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
	report.ReportError(err, sentry.LevelFatal)
	report.FlushSentry()
	logger.Error(err.Error())
	os.Exit(1)
}
