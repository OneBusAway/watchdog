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
	ConfigService  *config.ConfigService
	GtfsService    *gtfs.GtfsService
	MetricsService *metrics.MetricsService
	Logger         *slog.Logger
	Version        string
}

// New creates and wires all dependencies for the Application.
// Accepts config, logger, client, and version as arguments.
//
// Server-scope wiring (added with the redesign): the static store and route
// → agency index are now held by the Application so the metrics-collector can
// resolve scopes for both agency-mode and server-mode entries. The
// gtfs-service constructor receives the route index too so it can populate it
// during static-bundle parse.
func New(cfg *config.Config, logger *slog.Logger, client *http.Client, version string) *Application {

	staticStore := gtfs.NewStaticStore()
	realtimeStore := gtfs.NewRealtimeStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	routeAgencyIndex := gtfs.NewRouteAgencyIndex()
	vehicleLastSeen := metrics.NewVehicleLastSeen()
	unmatchedStopTracker := metrics.NewUnmatchedStopTracker()
	backoffStore := config.NewBackoffStore()

	obaSDKClientCache := NewObaSDKClientCache(client)
	configService := config.NewConfigService(logger, client, cfg, backoffStore)
	gtfsService := gtfs.NewGtfsService(staticStore, realtimeStore, boundingBoxStore, routeAgencyIndex, logger, client)
	metricsService := metrics.NewMetricsService(staticStore, realtimeStore, boundingBoxStore, routeAgencyIndex, vehicleLastSeen, unmatchedStopTracker, logger, client, obaSDKClientCache.For)

	// Wire the per-agency introspection gauge emission through a callback so
	// the metrics package doesn't need to import gtfs (and vice versa).
	gtfsService.SetBundleObserver(metricsService.StaticBundleObserver())

	return &Application{
		ConfigService:  configService,
		GtfsService:    gtfsService,
		MetricsService: metricsService,
		Logger:         logger,
		Version:        version,
	}
}
