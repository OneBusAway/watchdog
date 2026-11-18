package app

import (
	"fmt"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// What an expired static bundle means for collection:
//
//   - The schedule gauges report unknown (available=0, active=0), because an
//     expired bundle schedules nothing (metrics.ReportScheduledService).
//   - The vehicle pass, which attributes vehicles and checks bounds with the
//     static bundle, is skipped and its series are retired. In server mode the
//     pass is shared by every agency of the merged bundle, so it is skipped
//     only once every reported agency has expired.
//   - Everything that does not read the bundle keeps reporting: API ping,
//     OBA API metrics, GTFS-RT fetch status, and the expiration gauges.
//   - Sentry hears about it once when the bundle expires and once when it
//     recovers, never on every tick.

// Why an expired bundle is still in use.
const (
	// expiryCauseRefreshFailing: the last refresh failed, so a newer bundle
	// may exist that Watchdog could not download or validate.
	expiryCauseRefreshFailing = "refresh_failing"
	// expiryCauseNoNewerBundle: the last refresh succeeded and still produced
	// this bundle, so the agency has not published a newer one.
	expiryCauseNoNewerBundle = "no_newer_bundle"
)

// BundleExpiryReporter remembers which entries currently hold an expired
// static bundle, so a change in that state is reported exactly once.
type BundleExpiryReporter struct {
	mu      sync.Mutex
	expired map[string]bool
}

func NewBundleExpiryReporter() *BundleExpiryReporter {
	return &BundleExpiryReporter{expired: make(map[string]bool)}
}

// transition records the entry's state and reports whether it changed.
func (r *BundleExpiryReporter) transition(serverKey string, expired bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.expired[serverKey] == expired {
		return false
	}
	if expired {
		r.expired[serverKey] = true
	} else {
		delete(r.expired, serverKey)
	}
	return true
}

// deploymentExpired reports whether any agency entry of the deployment (a
// sanitized base URL) was expired when last observed.
func (r *BundleExpiryReporter) deploymentExpired(deployment string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.expired {
		if baseURL, _, ok := models.ParseServerKey(key); ok && baseURL == deployment {
			return true
		}
	}
	return false
}

// Prune drops the state of entries the keep predicate rejects.
func (r *BundleExpiryReporter) Prune(keep func(serverKey string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.expired {
		if !keep(key) {
			delete(r.expired, key)
		}
	}
}

// observeBundleExpiry reports a change in whether the agency entry's static
// bundle has expired. Call it once per agency entry per tick.
func (app *Application) observeBundleExpiry(server models.ObaServer, now time.Time) {
	expired := app.MetricsService.StaticBundleExpired(server, now)
	if !app.BundleExpiry.transition(server.ServerKey(), expired) {
		return
	}
	tags := map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
	}
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if !expired {
		app.Logger.Info("Static GTFS bundle is no longer expired; resuming schedule and vehicle metrics",
			"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", serverURL)
		report.ReportErrorWithSentryOptions(fmt.Errorf("static GTFS bundle for %s agency %s is no longer expired", serverURL, server.AgencyID),
			report.SentryReportOptions{Tags: tags, Level: sentry.LevelInfo})
		return
	}
	cause := expiryCauseRefreshFailing
	if state, ok := app.GtfsService.StaticStore.GetRefreshState(server); ok && state.Success {
		cause = expiryCauseNoNewerBundle
	}
	tags["cause"] = cause
	app.Logger.Error("Static GTFS bundle has expired; withholding schedule and vehicle metrics",
		"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", serverURL, "cause", cause)
	report.ReportErrorWithSentryOptions(fmt.Errorf("static GTFS bundle for %s agency %s has expired (%s)", serverURL, server.AgencyID, cause),
		report.SentryReportOptions{Tags: tags, ExtraContext: map[string]interface{}{"oba_base_url": serverURL}, Level: sentry.LevelFatal})
}

// staticBundlesExpired reports whether every given agency entry holds an
// expired static bundle. It is false for an empty list.
func (app *Application) staticBundlesExpired(entries []models.ObaServer, now time.Time) bool {
	for _, entry := range entries {
		if !app.MetricsService.StaticBundleExpired(entry, now) {
			return false
		}
	}
	return len(entries) > 0
}
