package main

import (
	"time"

	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

func (app *application) startMetricsCollection() {

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:

				app.mu.Lock()
				servers := app.config.Servers
				app.mu.Unlock()

				for _, server := range servers {
					app.collectMetricsForServer(server)
				}
			}
		}
	}()
}

func (app *application) collectMetricsForServer(server models.ObaServer) {
	metrics.ServerPing(server)
	cachePath, err := utils.GetLastCachedFile("cache", server.ID)
	if err != nil {
		app.logger.Error("Failed to get last cached file", "error", err)
		return
	}

	_, _, err = metrics.CheckBundleExpiration(cachePath, app.logger, time.Now(), server)
	if err != nil {
		app.logger.Error("Failed to check GTFS bundle expiration", "error", err)
	}

	err = metrics.CheckAgenciesWithCoverageMatch(cachePath, app.logger, server)

	if err != nil {
		app.logger.Error("Failed to check agencies with coverage match metric", "error", err)
	}

	err = metrics.CheckVehicleCountMatch(server)

	if err != nil {
		app.logger.Error("Failed to check vehicle count match metric", "error", err)
	}

	err = metrics.FetchObaAPIMetrics(server.AgencyID)

	if err != nil {
		app.logger.Error("Failed to fetch OBA API metrics", "error", err)
	}
}
