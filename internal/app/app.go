package app

import (
	"log/slog"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/server"
)

// Application struct holds the configuration, logger, reporter, and version for the watchdog application.
type Application struct {
	Config           server.Config
	Logger           *slog.Logger
	Version          string
	BoundingBoxStore *geo.BoundingBoxStore
}
