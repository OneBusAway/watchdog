package app

import (
	"context"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// StartMetricsCollection begins a background goroutine that continuously collects metrics
// from all configured OBA (OneBusAway) servers at a regular interval.
//
// It uses a time.Ticker based on the `FetchInterval` configured in the app's config came from "fetch-interval" command line flags.
// The ticker triggers every `FetchInterval` seconds, allowing the application to periodically
// collect and update metrics related to OBA servers listed in the config.
//
// The collection routine gracefully shuts down when the provided context is canceled,
// allowing the application to cleanly exit or restart.
//
// This function is the central entry point for periodic monitoring of external systems
// like GTFS static bundles, real-time GTFS-RT feeds, and OBA APIs.
//
// Purpose:
//   - Ensure consistent collection of operational, transit, and health-related metrics.
//   - Drive metrics exposed on Prometheus endpoints, used in dashboards and alerts.
//   - Monitor reliability and correctness of OBA and GTFS-RT server integrations.
//
// Behavior:
//   - If no servers are configured, the function silently waits and retries on next tick.
//   - On shutdown (context canceled), it logs the stop and exits the goroutine cleanly.
func (app *Application) StartMetricsCollection(ctx context.Context) {

	ticker := time.NewTicker(time.Duration(app.ConfigService.Config.FetchInterval) * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				app.Logger.Info("Stopping metrics collection routine")
				return
			case <-ticker.C:

				servers := app.ConfigService.Config.GetServers()

				for _, server := range servers {
					app.CollectMetricsForServer(server)
				}
			}
		}
	}()
}

// CollectMetricsForServer performs all metric collection and validation logic for a single OBA server.
//
// It sequentially runs a series of probes and validations against the given server:
//  1. Pings the server to track basic availability.
//  2. Checks GTFS static bundle expiration.
//  3. Verifies agency coverage match (GTFS static vs real-time).
//  4. Collects metrics from the OBA API endpoints.
//  5. Fetches and stores GTFS-RT (realtime) vehicle positions feed.
//  6. Validates consistency between expected and actual vehicle counts.
//  7. Tracks frequency of vehicle telemetry reporting over time.
//  8. Flags invalid vehicles and vehicles stopped outside bounds.
//
// Errors in each step are logged and reported to Sentry with contextual tags (e.g., server name, ID),
// but the process continues unless the GTFS-RT feed fails — in which case the function returns early,
// as later checks depend on the real-time data.
//
// Purpose:
//   - Centralizes all server-level metric gathering for reusability and testability.
//   - Ensures that all health and performance indicators are collected in one place.
//   - Enables observability and alerting based on up-to-date, per-server insights.
//
// Design considerations:
//   - Each metric function is isolated and logs its own errors to avoid full failure on one fault.
//   - Sentry reports are tagged for fast debugging and correlation in distributed systems.
//   - Dependencies are injected (via app fields) to support testability and separation of concerns.
func (app *Application) CollectMetricsForServer(server models.ObaServer) {
	app.MetricsService.ServerPing(server)

	_, _, err := app.MetricsService.CheckBundleExpiration(time.Now().UTC(), server)
	if err != nil {
		app.Logger.Error("Failed to check GTFS bundle expiration", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
	}

	err = app.MetricsService.CheckAgenciesWithCoverageMatch(server)

	if err != nil {
		app.Logger.Error("Failed to check agencies with coverage match metric", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
	}

	err = app.MetricsService.FetchObaAPIMetrics(server.AgencyID, server.ID, server.ObaBaseURL, server.ObaApiKey)

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
	err = app.GtfsService.FetchAndStoreGTFSRTFeed(server)
	if err != nil {
		app.Logger.Error("Failed to fetch and store GTFS-RT feed", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
		return
	}

	err = app.MetricsService.CheckVehicleCountMatch(server)
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

	err = app.MetricsService.TrackVehicleTelemetry(server)
	if err != nil {
		app.Logger.Error("Failed to track vehicle reporting frequency", "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": fmt.Sprintf("%d", server.ID),
			},
			Level: sentry.LevelError,
		})
	}

	err = app.MetricsService.TrackInvalidVehiclesAndStoppedOutOfBounds(server)
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
