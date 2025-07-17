package app

import (
	"log/slog"
	"sync"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
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
	Mu               sync.RWMutex
	Version          string
	BoundingBoxStore *geo.BoundingBoxStore
}

// updateConfig safely updates the application's server configuration.
func (app *Application) UpdateConfig(newServers []models.ObaServer) {
	app.Mu.Lock()
	defer app.Mu.Unlock()
	app.Config.Servers = newServers
}
