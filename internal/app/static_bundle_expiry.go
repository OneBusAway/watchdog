package app

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// When collection withholds the metrics derived from the static bundle:
//
//   - The bundle has expired: the agency-local date is past its latest
//     service date. An expired bundle schedules nothing and no longer
//     describes the vehicles the realtime feed reports.
//   - The backend is behind: Watchdog has already moved to a newer bundle and
//     the backend still serves the one it replaced. Data from one bundle must
//     not be validated against another.
//
// While withheld:
//
//   - The schedule gauges report unknown (available=0, active=0).
//   - The vehicle pass, which attributes vehicles and checks bounds with the
//     static bundle, is skipped and its series are retired. In server mode the
//     pass is shared by every agency of the merged bundle, so it is skipped
//     only once every reported agency is withheld.
//   - The unmatched-stop lookup is skipped per agency, and its unresolved
//     gauge is retired. Info and cluster series age out on their 24h TTL.
//   - Everything that does not read the bundle keeps reporting: API ping,
//     the other OBA API metrics, GTFS-RT fetch status, and the expiration
//     gauges.
//   - Sentry hears about it once (a fatal event, tagged with its cause) and
//     once more when it is over (an info event), never on every tick.
//
// An expired bundle's cause is decided only after Watchdog has checked the
// feed URL since the expiry (StaticBundleWatch downloads it at once), so the
// event never blames the agency or the backend from stale information.

// Why static-derived metrics are withheld. The Sentry event carries it as the
// cause tag.
const (
	// withholdCauseNoNewerBundle: the feed URL was checked after the expiry
	// and still serves an expired bundle. Watchdog and the backend are both
	// on it because the agency has not published a current one.
	withholdCauseNoNewerBundle = "no_newer_bundle"
	// withholdCauseFeedUnreachable: the feed URL check after the expiry
	// failed (the reason tag says how), and the backend serves the same
	// expired bundle or its hash is unknown. Neither side can get a current
	// bundle.
	withholdCauseFeedUnreachable = "feed_unreachable"
	// withholdCauseRefreshFailing: the feed URL check after the expiry failed
	// (the reason tag says how) while the backend serves a different bundle.
	// Watchdog cannot load the bundle the backend uses.
	withholdCauseRefreshFailing = "refresh_failing"
	// withholdCauseBackendStale: Watchdog holds a newer bundle from the feed
	// URL, and the backend still serves the one it replaced.
	withholdCauseBackendStale = "backend_stale"
	// withholdCauseFeedNotChecked: no feed URL check finished within
	// expiryCauseWait of the expiry, so the cause is unknown.
	withholdCauseFeedNotChecked = "feed_not_checked"
)

// expiryCauseWait is how long an expiry waits for a feed URL check to finish
// before it is reported without a known cause.
const expiryCauseWait = time.Hour

// feedCheckClockSkew lets a check that started in the same tick that noticed
// the expiry, a moment before the expiry was recorded, still count. A
// variable so tests can make an earlier check not count.
var feedCheckClockSkew = time.Minute

type withholdKind int

const (
	withholdNone withholdKind = iota
	withholdExpired
	withholdBackendBehind
)

type withheldEntry struct {
	kind          withholdKind
	since         time.Time
	debounceSince time.Time
	// reported records that the fatal event was sent, with its cause and reason.
	reported      bool
	cause, reason string
}

// BundleExpiryReporter remembers which entries currently withhold their
// static-derived metrics, and why, so each change is reported exactly once.
type BundleExpiryReporter struct {
	mu      sync.Mutex
	entries map[string]withheldEntry
}

func NewBundleExpiryReporter() *BundleExpiryReporter {
	return &BundleExpiryReporter{entries: make(map[string]withheldEntry)}
}

// update records the entry's kind and returns its state before and after.
func (r *BundleExpiryReporter) update(serverKey string, kind withholdKind, now time.Time) (before withheldEntry, after withheldEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before = r.entries[serverKey]
	if kind == withholdNone {
		delete(r.entries, serverKey)
		return before, withheldEntry{}
	}
	after = before
	if before.kind != kind {
		after = withheldEntry{kind: kind, since: now}
	}
	r.entries[serverKey] = after
	return before, after
}

func (r *BundleExpiryReporter) markReported(serverKey, cause, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[serverKey]; ok {
		entry.reported, entry.cause, entry.reason = true, cause, reason
		r.entries[serverKey] = entry
	}
}

func (r *BundleExpiryReporter) debounceSince(serverKey string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[serverKey].debounceSince
}

func (r *BundleExpiryReporter) setDebounceSince(serverKey string, t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[serverKey]; ok {
		entry.debounceSince = t
		r.entries[serverKey] = entry
	}
}

// entry returns the entry's current state.
func (r *BundleExpiryReporter) entry(serverKey string) withheldEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[serverKey]
}

// deploymentExpired reports whether any agency entry of the deployment (a
// sanitized base URL) held an expired bundle when last observed. An entry
// whose snapshot is gone (present reports false) is dropped instead: an
// expired agency removed from a merged bundle is never observed again, and
// its record must not keep the whole deployment on the 2-hour schedule.
func (r *BundleExpiryReporter) deploymentExpired(deployment string, present func(serverKey string) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	expired := false
	for key, entry := range r.entries {
		baseURL, _, ok := models.ParseServerKey(key)
		if !ok || baseURL != deployment {
			continue
		}
		if !present(key) {
			delete(r.entries, key)
			continue
		}
		if entry.kind == withholdExpired {
			expired = true
		}
	}
	return expired
}

// Prune drops the state of entries the keep predicate rejects.
func (r *BundleExpiryReporter) Prune(keep func(serverKey string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		if !keep(key) {
			delete(r.entries, key)
		}
	}
}

// debounceWithholdMax is how long static metrics may remain withheld
// while awaiting backend hash confirmation before falling back to withholdNone.
// A variable so tests need not wait for it.
var debounceWithholdMax = 10 * time.Minute

// staticWithholdKind decides whether the entry's static-derived metrics are
// withheld now, and why.
func (app *Application) staticWithholdKind(server models.ObaServer, now time.Time) withholdKind {
	if app.MetricsService.StaticBundleExpired(server, now) {
		if app.BundleExpiry != nil {
			app.BundleExpiry.setDebounceSince(server.ServerKey(), time.Time{})
		}
		return withholdExpired
	}
	relation, debouncing := app.GtfsService.BackendBundleSyncState(server)
	if relation == gtfs.BackendBundleBehind {
		return withholdBackendBehind
	}
	if debouncing && app.BundleExpiry != nil {
		if prior := app.BundleExpiry.entry(server.ServerKey()).kind; prior != withholdNone {
			debounceStart := app.BundleExpiry.debounceSince(server.ServerKey())
			if debounceStart.IsZero() {
				app.BundleExpiry.setDebounceSince(server.ServerKey(), now)
				debounceStart = now
			}
			if now.Sub(debounceStart) >= debounceWithholdMax {
				app.Logger.Warn("Static GTFS bundle debounce timed out after sustained metadata failures; falling back to withholdNone",
					"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", utils.SanitizeServerURL(server.ObaBaseURL))
				app.GtfsService.ClearBackendDebounce(server)
				if rel, _ := app.GtfsService.BackendBundleSyncState(server); rel == gtfs.BackendBundleBehind {
					return withholdBackendBehind
				}
				return withholdNone
			}
			return prior
		}
	}
	return withholdNone
}

// staticMetricsWithheld reports whether the entry's static-derived metrics
// are withheld now.
func (app *Application) staticMetricsWithheld(server models.ObaServer, now time.Time) bool {
	return app.staticWithholdKind(server, now) != withholdNone
}

// staticMetricsWithheldForAll reports whether every given agency entry
// withholds its static-derived metrics. It is false for an empty list.
func (app *Application) staticMetricsWithheldForAll(entries []models.ObaServer, now time.Time) bool {
	for _, entry := range entries {
		if !app.staticMetricsWithheld(entry, now) {
			return false
		}
	}
	return len(entries) > 0
}

// observeStaticBundle reports a change in whether the agency entry withholds
// its static-derived metrics. Call it once per agency entry per tick.
func (app *Application) observeStaticBundle(server models.ObaServer, now time.Time) {
	kind := app.staticWithholdKind(server, now)
	before, after := app.BundleExpiry.update(server.ServerKey(), kind, now)
	tags := map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
	}
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if kind == withholdNone {
		if before.kind == withholdNone {
			return
		}
		app.Logger.Info("Static GTFS bundle is current and matches the backend; resuming schedule and vehicle metrics",
			"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", serverURL)
		if before.reported {
			report.ReportErrorWithSentryOptions(fmt.Errorf("static GTFS bundle for %s agency %s is usable again", serverURL, server.AgencyID),
				report.SentryReportOptions{Tags: tags, Level: sentry.LevelInfo})
		}
		return
	}
	if after.reported {
		return
	}
	cause, reason, decided := app.withholdCause(server, after, now)
	if !decided {
		if before.kind != kind {
			app.Logger.Warn("Static GTFS bundle has expired; withholding schedule and vehicle metrics while the feed URL is checked",
				"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", serverURL)
		}
		return
	}
	app.BundleExpiry.markReported(server.ServerKey(), cause, reason)
	tags["cause"] = cause
	logArgs := []any{"agency_id", server.AgencyID, "server_name", server.ServerName, "server_url", serverURL, "cause", cause}
	if reason != "" {
		tags["reason"] = reason
		logArgs = append(logArgs, "reason", reason)
	}
	summary := "has expired"
	if kind == withholdBackendBehind {
		summary = "is newer than the backend's"
	}
	app.Logger.Error("Static GTFS bundle "+summary+"; withholding schedule and vehicle metrics", logArgs...)
	message := fmt.Sprintf("static GTFS bundle for %s agency %s %s (%s)", serverURL, server.AgencyID, summary, cause)
	if reason != "" {
		message = fmt.Sprintf("static GTFS bundle for %s agency %s %s (%s: %s)", serverURL, server.AgencyID, summary, cause, reason)
	}
	report.ReportErrorWithSentryOptions(errors.New(message),
		report.SentryReportOptions{Tags: tags, ExtraContext: map[string]interface{}{"oba_base_url": serverURL}, Level: sentry.LevelFatal})
}

// withholdCause decides why the entry is withheld. decided is false while an
// expired bundle still waits for a feed URL check that started after it
// expired.
func (app *Application) withholdCause(server models.ObaServer, entry withheldEntry, now time.Time) (cause, reason string, decided bool) {
	if entry.kind == withholdBackendBehind {
		return withholdCauseBackendStale, "", true
	}
	check, ok := app.GtfsService.LastStaticFeedCheck(server)
	if !ok || check.At.Before(entry.since.Add(-feedCheckClockSkew)) {
		// Every check so far predates the expiry: it cannot say whether the
		// agency has published a newer bundle since.
		if now.Sub(entry.since) < expiryCauseWait {
			return "", "", false
		}
		return withholdCauseFeedNotChecked, "", true
	}
	if check.Success {
		return withholdCauseNoNewerBundle, "", true
	}
	if app.GtfsService.BackendBundleRelation(server) == gtfs.BackendBundleDiffers {
		return withholdCauseRefreshFailing, string(check.Reason), true
	}
	return withholdCauseFeedUnreachable, string(check.Reason), true
}
