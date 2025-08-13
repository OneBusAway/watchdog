package app

import (
	"log/slog"
	"net/http"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
)

// Application represents the main application structure.
// It holds references to the configuration service, GTFS service, metrics service,
// logger, and the application version.
// This structure is used to wire all dependencies together and provide a clean API for the application.
// It is initialized with the necessary services and can be used to start the application.
type Application struct {
	ConfigService    *config.ConfigService
	GtfsService      *gtfs.GtfsService
	MetricsService   *metrics.MetricsService
	Logger           *slog.Logger
	Version          string
}

// New creates and wires all dependencies for the Application.
// Accepts config, logger, client, and version as arguments.
func New(cfg *config.Config, logger *slog.Logger, client *http.Client, version string) *Application {
	
	staticStore := gtfs.NewStaticStore()
	realtimeStore := gtfs.NewRealtimeStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	vehicleLastSeen := metrics.NewVehicleLastSeen()

	configService := config.NewConfigService(logger, client, cfg)
	gtfsService := gtfs.NewGtfsService(staticStore,realtimeStore,boundingBoxStore,logger, client)
	metricsService := metrics.NewMetricsService(staticStore,realtimeStore,boundingBoxStore,vehicleLastSeen,logger,client)
	
	return &Application{
		ConfigService:    configService,
		GtfsService:      gtfsService,
		MetricsService:   metricsService,
		Logger:           logger,
		Version:          version,
	}
}
