package app

import (
	"context"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func (app *Application) StartMetricsCollection(ctx context.Context) {

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				app.Logger.Info("Stopping metrics collection routine")
				return
			case <-ticker.C:

				servers := app.Config.GetServers()

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

	_, _, err = metrics.CheckBundleExpiration(cachePath, time.Now(), server)
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

	err = metrics.FetchObaAPIMetrics(server.AgencyID, server.ID, server.ObaBaseURL, server.ObaApiKey, app.Client)

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
	// Fetch and store GTFS-RT feed
	// Note : function after FetchAndStoreGTFSRTFeed is depends on this function
	// on failure of this function we return
	err = gtfs.FetchAndStoreGTFSRTFeed(server, app.RealtimeStore, app.Client)
	if err != nil {
		app.Logger.Error("Failed to fetch and store GTFS-RT feed", "error", err)
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
		return
	}

	err = metrics.CheckVehicleCountMatch(server, app.RealtimeStore)
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

	err = metrics.TrackVehicleTelemetry(server, app.VehicleLastSeen, app.RealtimeStore)
	if err != nil {
		app.Logger.Error("Failed to track vehicle reporting frequency", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": fmt.Sprintf("%d", server.ID),
			},
			Level: sentry.LevelError,
		})
	}

	err = metrics.TrackInvalidVehiclesAndStoppedOutOfBounds(server, app.BoundingBoxStore, app.RealtimeStore)
	if err != nil {
		app.Logger.Error("Failed to count invalid vehicle coordinates", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": fmt.Sprintf("%d", server.ID),
			},
			Level: sentry.LevelError,
		})
	}

}
