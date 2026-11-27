package app

import (
	"context"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
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
// collection: HistoryService logs and reports them. A server-scoped response
// is recorded even when no agency check ran: OBA listing no agency at all is
// exactly what closes the open episodes of agencies it stopped listing
// (#438).
func (app *Application) recordOBAMetricsHistory(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time) {
	if app.obaMetricsHistory == nil || response == nil {
		return
	}
	if len(agencies) == 0 && !server.IsServerScoped() {
		return
	}
	app.obaMetricsHistory.RecordOBAMetrics(ctx, server, agencies, response, observedAt)
}

// vehiclePositionRecorder records the vehicle anomalies of one realtime
// snapshot in investigation history. *history.HistoryService implements it;
// tests substitute a fake.
type vehiclePositionRecorder interface {
	RecordVehiclePositions(ctx context.Context, server models.ObaServer, snapshot *models.RealtimeData, attributor metrics.VehicleAttributor, boxes *geo.BoundingBoxStore, lastSeen *metrics.VehicleLastSeen)
}

// recordVehicleHistory records the vehicle anomalies of the snapshot the
// vehicle passes just read, attributing vehicles with the same resolver. It is
// a no-op when investigation storage is disabled, and its failures never stop
// collection: HistoryService logs and reports them.
func (app *Application) recordVehicleHistory(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, resolver gtfs.VehicleAttributionResolver) {
	if app.vehicleHistory == nil {
		return
	}
	snapshot := app.MetricsService.RealtimeStore.Get(server.ServerKey())
	if snapshot == nil {
		return
	}
	attributor := metrics.NewVehicleAttributor(server, agencies, resolver)
	app.vehicleHistory.RecordVehiclePositions(ctx, server, snapshot, attributor, app.MetricsService.BoundingBoxStore, app.MetricsService.VehicleLastSeen)
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

// ReconcileHistoryAtStartup closes persisted unmatched-stop episodes whose
// agency left the configuration while Watchdog was down. A remote-config
// deployment gets the same reconciliation from the first refresh's
// OnConfigUpdated; a --config-file deployment has no refresh loop, so without
// this the episodes would stay open — and be reported open by the
// investigation API — until the agency returns (#438). Server-scoped entries
// record every agency OBA reports, so only agency-mode entries constrain
// which agencies are kept.
func (app *Application) ReconcileHistoryAtStartup(ctx context.Context, servers []models.ObaServer) {
	if app.HistoryService == nil {
		return
	}
	configuredAgencies := make(map[string]map[string]bool)
	serverByURL := make(map[string]models.ObaServer)
	serverScopedURLs := make(map[string]bool)
	for _, server := range servers {
		if server.IsServerScoped() {
			serverScopedURLs[utils.SanitizeServerURL(server.ObaBaseURL)] = true
		}
	}
	for _, server := range servers {
		baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
		// One base URL can carry both a server-scoped entry and agency-mode
		// entries; the server-scoped entry records every agency OBA reports,
		// so its agencies must never be closed as removed. flushRemovedServer-
		// History guards the same case with its serverScopedURLs set.
		if baseURL == "" || server.IsServerScoped() || serverScopedURLs[baseURL] {
			continue
		}
		if configuredAgencies[baseURL] == nil {
			configuredAgencies[baseURL] = make(map[string]bool)
		}
		configuredAgencies[baseURL][server.AgencyID] = true
		serverByURL[baseURL] = server
	}
	for baseURL, keep := range configuredAgencies {
		ctx, cancel := context.WithTimeout(ctx, historyFlushTimeout)
		app.HistoryService.ReconcileConfiguredAgencies(ctx, serverByURL[baseURL], keep)
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
