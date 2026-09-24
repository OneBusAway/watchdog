package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/app"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// Application version injected at build time via ldflags.
// Defaults to "dev" when built without ldflags (e.g., go run).
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	var cfg config.Config

	fs := flag.NewFlagSet("watchdog", flag.ContinueOnError)
	fs.IntVar(&cfg.Port, "port", 4000, "API server port")
	fs.StringVar(&cfg.Env, "env", "development", "Environment (development|staging|production)")
	fs.IntVar(&cfg.FetchInterval, "fetch-interval", 30, "Interval (in seconds) at which the application fetches data from realtime APIs and updates Prometheus metrics")

	showVersion := fs.Bool("version", false, "display version and exit")
	configFile := fs.String("config-file", "", "Path to a local JSON configuration file")
	configURL := fs.String("config-url", "", "URL to a remote JSON configuration file")
	
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	logger.Info("Starting OneBusAway Watchdog", "version", version)
	configAuthUser := os.Getenv("CONFIG_AUTH_USER")
	configAuthPass := os.Getenv("CONFIG_AUTH_PASS")

	err := config.ValidateConfigFlags(configFile, configURL)
	if err != nil {
		logger.Error("Error validating config flags", "err", err)
		fs.Usage()
		return err
	}

	client := app.NewPooledClient()

	report.SetupSentry()
	defer report.FlushSentry()
	report.ConfigureScope(cfg.Env, version)

	droppedStore := config.NewDroppedServersStore()
	var servers []models.ObaServer
	if *configFile != "" {
		servers, err = config.LoadConfigFromFile(*configFile, logger, droppedStore)
	} else if *configURL != "" {
		servers, err = config.LoadConfigFromURL(ctx, client, *configURL, configAuthUser, configAuthPass, 20, logger, droppedStore)
	}

	if err != nil {
		logger.Error("Error loading configuration", "err", err)
		return err
	}

	if len(servers) == 0 {
		logger.Error("Error: No servers found in configuration.")
		return errors.New("no servers found in configuration")
	}

	cfg.UpdateConfig(servers)

	application := app.New(&cfg, logger, client, version, droppedStore)

	application.MetricsService.ReportTrackedAgencies(servers)

	application.GtfsService.DownloadGTFSBundles(ctx, servers, 20)
	application.StartMetricsCollection(ctx)

	go application.GtfsService.RefreshGTFSBundles(ctx, application.ConfigService.Config.GetServers, 24*time.Hour, 5)
	go application.MetricsService.VehicleLastSeen.ClearRoutine(ctx, 15*time.Minute, time.Hour)
	go application.MetricsService.UnmatchedStopTracker.ClearRoutine(ctx, 15*time.Minute, 24*time.Hour)

	if *configURL != "" {
		go application.ConfigService.RefreshConfig(ctx, *configURL, configAuthUser, configAuthPass, time.Minute, 20, func(updated []models.ObaServer) {
			application.OnConfigUpdated(ctx, updated)
		})
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      application.Routes(ctx),
		IdleTimeout:  time.Minute,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	logger.Info("starting server", "addr", srv.Addr, "env", cfg.Env)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("Shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			report.ReportError(err, sentry.LevelError)
			logger.Error("HTTP server shutdown failed", "error", err)
		}
	case err := <-serverErr:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		report.ReportError(err, sentry.LevelFatal)
		report.FlushSentry()
		logger.Error(err.Error())
		return err
	}
	return nil
}
