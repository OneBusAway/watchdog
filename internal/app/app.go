package app

import (
	"log/slog"
	"sync"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/server"
)

// Application struct holds the configuration, logger, reporter, and version for the watchdog application.
type Application struct {
	Config   server.Config
	Logger   *slog.Logger
	Reporter *report.Reporter
	Mu       sync.RWMutex
	Version  string
}

// updateConfig safely updates the application's server configuration.
func (app *Application) UpdateConfig(newServers []models.ObaServer) {
	app.Mu.Lock()
	defer app.Mu.Unlock()
	app.Config.Servers = newServers
}
