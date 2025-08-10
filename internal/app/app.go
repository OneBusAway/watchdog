package app

import (
	"log/slog"
	"net/http"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
)

// Application holds the shared dependencies for HTTP handlers, helpers, and middleware.
//
// Fields:
// - Config: The application configuration.
// - Logger: Structured logger used across the app.
// - Version: The current version of the application.
// - BoundingBoxStore: Responsible for calculating and storing bounding boxes for GTFS stops.
//
// This struct will expand as more components and dependencies are added during development.
type Application struct {
	Config           *config.Config
	Logger           *slog.Logger
	Client           *http.Client
	Version          string
	BoundingBoxStore *geo.BoundingBoxStore
	VehicleLastSeen  *metrics.VehicleLastSeen
	RealtimeStore    *gtfs.RealtimeStore
	StaticStore      *gtfs.StaticStore
}
