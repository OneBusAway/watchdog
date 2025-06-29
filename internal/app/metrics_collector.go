package app

import (
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func (app *Application) StartMetricsCollection() {

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:

				app.Mu.Lock()
				servers := app.Config.Servers
				app.Mu.Unlock()

				for _, server := range servers {
					app.CollectMetricsForServer(server)
				}
			}
		}
	}()
}

func (app *Application) CollectMetricsForServer(server models.ObaServer) {
	metrics.ServerPing(server)
	cachePath, err := utils.GetLastCachedFile("cache", server.ID)
	if err != nil {
		app.Logger.Error("Failed to get last cached file", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
		return
	}

	_, _, err = metrics.CheckBundleExpiration(cachePath, app.Logger, time.Now(), server)
	if err != nil {
		app.Logger.Error("Failed to check GTFS bundle expiration", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			ExtraContext: map[string]interface{}{
				"cache_file": cachePath,
			},
			Level: sentry.LevelError,
		})
	}

	err = metrics.CheckAgenciesWithCoverageMatch(cachePath, app.Logger, server)

	if err != nil {
		app.Logger.Error("Failed to check agencies with coverage match metric", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			ExtraContext: map[string]interface{}{
				"cache_file": cachePath,
			},
			Level: sentry.LevelError,
		})
	}

	err = metrics.CheckVehicleCountMatch(server)

	if err != nil {
		app.Logger.Error("Failed to check vehicle count match metric", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
	}

	err = metrics.FetchObaAPIMetrics(server.AgencyID, server.ID, server.ObaBaseURL, server.ObaApiKey, nil)

	if err != nil {
		app.Logger.Error("Failed to fetch OBA API metrics", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
			Level: sentry.LevelError,
		})
	}
	err = metrics.TrackVehicleReportingFrequency(server)
	if err != nil {
		app.Logger.Error("Failed to track vehicle reporting frequency", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": fmt.Sprintf("%d", server.ID),
			},
			Level: sentry.LevelError,
		})
	}

}
