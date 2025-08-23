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
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// Declare a string containing the application version number. Later in the book we'll
// generate this automatically at build time, but for now we'll just store the version
// number as a hard-coded global constant.
const version = "1.0.0"

func main() {
	// Initialize a structured logger for the application
	// This logger will be used throughout the application for logging messages.
	// It can be configured to log to different outputs (e.g., console, file)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Load environment variables for configuration
	configAuthUser := os.Getenv("CONFIG_AUTH_USER")
	configAuthPass := os.Getenv("CONFIG_AUTH_PASS")

	// Initialize the application configuration with default values
	// These values can be overridden by command line flags or environment variables.
	var cfg config.Config

	flag.IntVar(&cfg.Port, "port", 4000, "API server port")
	flag.StringVar(&cfg.Env, "env", "development", "Environment (development|staging|production)")
	flag.IntVar(&cfg.FetchInterval, "fetch-interval", 30, "Interval (in seconds) at which the application fetches data from realtime APIs and updates Prometheus metrics")

	var (
		configFile = flag.String("config-file", "", "Path to a local JSON configuration file")
		configURL  = flag.String("config-url", "", "URL to a remote JSON configuration file")
	)
	// Parse command line flags
	flag.Parse()

	// Validate that only one configuration source is specified
	// Either a config file or a remote config URL can be specified, but not both.
	err := config.ValidateConfigFlags(configFile, configURL)
	if err != nil {
		logger.Error("Error validating config flags", "err", err)
		flag.Usage()
		os.Exit(1)
	}

	// At this point, we are sure that all command line flags have been parsed
	// and we can proceed with the application initialization.

	// Create a new HTTP client with a connection pool
	// This client will be reused across the application to avoid creating new connections for each request.
	// This is particularly useful for polling APIs like GTFS-RT endpoints.
	// It can be configured with timeouts, retries, etc.
	// Using a pooled client allows for better performance and resource management.
	client := app.NewPooledClient()

	// Load the configuration from the specified source
	// If a config file is specified, load it from disk.
	// If a config URL is specified, fetch it over HTTP(S).
	var servers []models.ObaServer
	if *configFile != "" {
		servers, err = config.LoadConfigFromFile(*configFile)
	} else if *configURL != "" {
		servers, err = config.LoadConfigFromURL(client, *configURL, configAuthUser, configAuthPass)
	}

	if err != nil {
		logger.Error("Error loading configuration", "err", err)
		os.Exit(1)
	}

	if len(servers) == 0 {
		logger.Error("Error: No servers found in configuration.")
		os.Exit(1)
	}

	cfg.UpdateConfig(servers)

	// At this point, we have successfully loaded the configuration
	// and have a list of OBA servers to work with.

	// Initialize the application struct with all services
	// This includes the configuration service, GTFS service, and metrics service.
	// and the required dependencies.
	// this New() function is critical in understanding how we structure the application talk a look at it.
	// and also take a look at service file in each package to see the dependencies and the exposed methods and function.
	app := app.New(&cfg, logger, client, version)

	// Initialize Sentry for error reporting
	// This will allow us to capture and report errors that occur during the application's execution.
	// Sentry is a powerful error tracking tool that helps developers monitor and fix crashes in real-time.
	// It provides detailed error reports, stack traces, and context about the errors.
	//
	// Note: you should have (SENTRY_DSN) environment variable set to your Sentry DSN.
	// you can read in sentry documentation how to set it up.
	// Link to official documentation: https://docs.sentry.io/concepts/key-terms/dsn-explainer/
	report.SetupSentry()
	defer report.FlushSentry()
	report.ConfigureScope(cfg.Env, version)

	// Create a context for the application
	// This context will be used to manage the application's lifecycle and cancel operations when needed.
	// It allows us to gracefully shut down the application and clean up resources.
	// we will use it to cancel and clean up routines when the application is shutting down.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// From here we set up all dependencies and we are ready to start business logic.

	// On startup, download GTFS static bundles for all configured servers
	app.GtfsService.DownloadGTFSBundles(servers)

	// This function starts the metrics collection process
	// it intialize a routine the run every FetchInterval seconds (30 seconds by default)
	// and collects metrics from all configured OBA servers.
	app.StartMetricsCollection(ctx)

	// Cron job to download GTFS bundles for all servers every 24 hours
	go app.GtfsService.RefreshGTFSBundles(ctx, servers, 24*time.Hour)

	// Cron job to delete the data of vehicles that has not sent updates for 1 hour
	go app.MetricsService.VehicleLastSeen.ClearRoutine(ctx, 15*time.Minute, time.Hour)

	// If a remote URL is specified, refresh the configuration every minute
	if *configURL != "" {
		go app.ConfigService.RefreshConfig(ctx, *configURL, configAuthUser, configAuthPass, time.Minute)
	}

	// Start the HTTP server to serve the API and metrics endpoints
	// take a look at the app.Routes() function to see how we set up the routes.
	// This function returns an http.Handler that contains all the routes and middleware for the application.
	// The server will listen on the port specified in the configuration (4000 by default).
	// The server will handle incoming HTTP requests and route them to the appropriate handlers.
	// It will also expose Prometheus metrics at /metrics endpoint.
	// There is also a health check endpoint at /health that returns 200 OK if the server is running.

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      app.Routes(ctx),
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
