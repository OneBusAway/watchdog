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
// unmatched-stop episodes for servers no longer configured, and drops those
// servers from history's memory.
func (app *Application) flushRemovedServerHistory(servers []models.ObaServer) {
	if app.HistoryService == nil {
		return
	}
	configuredURLs := make(map[string]bool, len(servers))
	for _, server := range servers {
		configuredURLs[utils.SanitizeServerURL(server.ObaBaseURL)] = true
	}
	for _, baseURL := range app.HistoryService.TrackedServerURLs() {
		if configuredURLs[baseURL] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), historyFlushTimeout)
		app.HistoryService.FlushServer(ctx, baseURL)
		cancel()
	}
}

// FlushHistory waits for an in-flight collection cycle to finish, then saves
// the last-reported times of every open unmatched-stop episode. main calls it
// on clean shutdown, after the collection context is canceled.
func (app *Application) FlushHistory() {
	if app.HistoryService == nil {
		return
	}
	app.collectMu.Lock()
	defer app.collectMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), historyFlushTimeout)
	defer cancel()
	app.HistoryService.Flush(ctx)
}
