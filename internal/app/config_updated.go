package app

import (
	"context"
	"fmt"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// OnConfigUpdated reacts to a freshly loaded remote configuration: it refreshes
// the tracked-agency gauges, retires everything belonging to servers that have
// left, and kicks an immediate bundle download for servers that have just
// joined so they do not wait out the 24h refresh.
//
// It lives here rather than as a closure in main so that the ordering and the
// empty-config guard below are under test; main has no test coverage.
//
// The empty-config guard is the important part. RefreshConfig invokes this on
// every successful load, and decodeServers drops entries that fail validation
// rather than failing the whole document, so a config endpoint that briefly
// serves "[]" — or one whose schema change blanks a required field on every
// entry — arrives here as an empty slice indistinguishable from "the operator
// removed every server." Acting on it would discard every parsed GTFS bundle,
// bounding box and route index, retire the entire /metrics surface (firing
// absent-series alerts fleet-wide), and then re-download every static bundle
// on the next refresh. main refuses to start with zero servers for the same
// reason; a refresh should not do what start-up rejects.
//
// The cost of ignoring a genuine "monitor nothing" config is that stale state
// lingers until the operator adds a server back or the process restarts, which
// is strictly better than the alternative.
func (app *Application) OnConfigUpdated(ctx context.Context, updated []models.ObaServer) {
	if len(updated) == 0 {
		err := fmt.Errorf("refreshed configuration contained no valid servers; ignoring it rather than pruning every server")
		app.Logger.Warn("Ignoring empty refreshed configuration; nothing will be pruned")
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Level: sentry.LevelWarning,
		})
		return
	}

	app.MetricsService.ReportTrackedAgencies(updated)

	// Servers that left the config keep their store entries and their
	// Prometheus series until they are explicitly retired.
	app.PruneStaleServers(updated)

	// Servers that just joined would otherwise wait for the next 24h refresh
	// before any static-derived metric appeared for them.
	if newcomers := app.NewlyAddedServers(updated); len(newcomers) > 0 {
		go app.GtfsService.DownloadGTFSBundles(ctx, newcomers, 5)
	}
}
