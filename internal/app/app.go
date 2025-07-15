package app

import (
	"log/slog"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/server"
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
	Config           server.Config
	Logger           *slog.Logger
	Version          string
	BoundingBoxStore *geo.BoundingBoxStore
}
