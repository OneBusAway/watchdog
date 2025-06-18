package main

import (
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
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
	metrics.ServerPing(server, app.reporter)
	cachePath, err := utils.GetLastCachedFile("cache", server.ID)
	if err != nil {
		app.logger.Error("Failed to get last cached file", "error", err)
		app.reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
		return
	}

	_, _, err = metrics.CheckBundleExpiration(cachePath, app.logger, time.Now(), server, app.reporter)
	if err != nil {
		app.logger.Error("Failed to check GTFS bundle expiration", "error", err)
		app.reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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

	err = metrics.CheckAgenciesWithCoverageMatch(cachePath, app.logger, server, app.reporter)

	if err != nil {
		app.logger.Error("Failed to check agencies with coverage match metric", "error", err)
		app.reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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

	err = metrics.CheckVehicleCountMatch(server, app.reporter)

	if err != nil {
		app.logger.Error("Failed to check vehicle count match metric", "error", err)
		app.reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":   fmt.Sprintf("%d", server.ID),
				"server_name": server.Name,
			},
			Level: sentry.LevelError,
		})
	}

	err = metrics.FetchObaAPIMetrics(server.AgencyID, server.ObaBaseURL, server.ObaApiKey, nil, app.reporter)

	if err != nil {
		app.logger.Error("Failed to fetch OBA API metrics", "error", err)
		app.reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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
}
