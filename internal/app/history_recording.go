package app

import (
	"context"
	"time"

	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// historyFlushTimeout bounds a flush of investigation history run outside a
// collection step: on server removal and on shutdown.
const historyFlushTimeout = 5 * time.Second

// obaMetricsRecorder records one OBA metrics response in investigation
// history. *history.HistoryService implements it; tests substitute a fake.
type obaMetricsRecorder interface {
	RecordOBAMetrics(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time)
}

// recordOBAMetricsHistory records a successful metrics.json response for the
// agencies whose checks ran on it. It is a no-op when investigation storage
// is disabled (app.obaMetricsHistory is nil), and its failures never stop
// collection: HistoryService logs and reports them.
func (app *Application) recordOBAMetricsHistory(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time) {
	if app.obaMetricsHistory == nil || response == nil || len(agencies) == 0 {
		return
	}
	app.obaMetricsHistory.RecordOBAMetrics(ctx, server, agencies, response, observedAt)
}

// flushRemovedServerHistory saves the last-reported times of open
// unmatched-stop episodes that no configured entry records any more, and
// drops them from history's memory: a whole server when its base URL is gone,
// and the removed agencies of a base URL that only agency-mode entries still
// configure. A server-scoped entry records every agency OBA reports, so its
// base URL keeps all of them.
func (app *Application) flushRemovedServerHistory(servers []models.ObaServer) {
	if app.HistoryService == nil {
		return
	}
	configuredURLs := make(map[string]bool, len(servers))
	serverScopedURLs := make(map[string]bool)
	configuredAgencies := make(map[string]map[string]bool)
	for _, server := range servers {
		baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
		configuredURLs[baseURL] = true
		if server.IsServerScoped() {
			serverScopedURLs[baseURL] = true
			continue
		}
		if configuredAgencies[baseURL] == nil {
			configuredAgencies[baseURL] = make(map[string]bool)
		}
		configuredAgencies[baseURL][server.AgencyID] = true
	}
	for _, baseURL := range app.HistoryService.TrackedServerURLs() {
		if serverScopedURLs[baseURL] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), historyFlushTimeout)
		if configuredURLs[baseURL] {
			app.HistoryService.FlushAgencies(ctx, baseURL, configuredAgencies[baseURL])
		} else {
			app.HistoryService.FlushServer(ctx, baseURL)
		}
		cancel()
	}
}

// FlushHistory waits for an in-flight collection cycle to finish, then saves
// the last-reported times of every open unmatched-stop episode. main calls it
// on clean shutdown, after the collection context is canceled. With
// investigation storage disabled there is nothing to save, so it returns
// without waiting for the cycle.
func (app *Application) FlushHistory() {
	if app.HistoryService == nil || app.InvestigationDB == nil {
		return
	}
	app.collectMu.Lock()
	defer app.collectMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), historyFlushTimeout)
	defer cancel()
	app.HistoryService.Flush(ctx)
}
