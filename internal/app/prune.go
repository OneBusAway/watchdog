package app

import (
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// PruneStaleServers drops every trace of servers that are no longer in the
// configuration: their entries in the in-memory stores, and the Prometheus
// series they published.
//
// It is called after a successful --config-url refresh. Without it a server
// removed from the config keeps its parsed bundle, bounding box, realtime
// feed, route index, last-seen vehicles, tracked unmatched stops and backoff
// state resident for the life of the process, and /metrics keeps advertising
// it — frozen at whatever value it last reported, which reads to an alert as
// a healthy server rather than an absent one.
//
// Backoff state is worth dropping for a second reason: it has no TTL of its
// own, so a server that is later re-added would otherwise inherit a stale
// NextRetryAt and be skipped for its first collection ticks before it has
// failed at anything.
//
// The keep rule follows the two scoping modes:
//
//   - An agency-scoped entry (agency_id set) owns exactly one serverKey.
//   - A server-scoped entry owns every key under its oba_base_url, because
//     its agencies are discovered from agency.txt and never appear in the
//     config at all.
//
// Both kinds can share an oba_base_url, so a base URL claimed by any
// server-scoped entry keeps all of its keys.
func (app *Application) PruneStaleServers(servers []models.ObaServer) {
	configuredURLs := make(map[string]bool, len(servers))
	for _, server := range servers {
		configuredURLs[utils.SanitizeServerURL(server.ObaBaseURL)] = true
	}

	// models.ObaServer.OwnsServerKey is the single definition of the ownership
	// rule described above; keeping it there means this predicate, the scope
	// resolver, and anything added later cannot drift apart on it.
	keep := func(serverKey string) bool {
		for _, server := range servers {
			if server.OwnsServerKey(serverKey) {
				return true
			}
		}
		return false
	}

	// Removed keys drive the per-agency series retirement at the bottom: an
	// agency that left a server which is itself still configured has no other
	// signal that it is gone.
	removed := make(map[string]bool)
	for _, keys := range [][]string{
		app.GtfsService.StaticStore.Prune(keep),
		app.GtfsService.RealtimeStore.Prune(keep),
		app.GtfsService.BoundingBoxStore.Prune(keep),
		app.MetricsService.VehicleLastSeen.Prune(keep),
		app.MetricsService.UnmatchedStopTracker.Prune(keep),
		app.ConfigService.BackoffStore.Prune(keep),
	} {
		for _, key := range keys {
			removed[key] = true
		}
	}

	// The route index is keyed by the raw oba_base_url the writer used, so
	// sanitize before comparing against the configured set.
	app.GtfsService.RouteAgencyIndex.PruneServers(func(baseURL string) bool {
		return configuredURLs[utils.SanitizeServerURL(baseURL)]
	})

	// Retire the metric series. A server that left entirely loses everything
	// under its server_url; an agency that left a server which is still
	// configured loses only its own agency-labelled series, so the server's
	// own gauges (oba_api_status and friends) keep reporting.
	//
	// Server-level retirement asks KnownServers rather than reading the keys
	// removed just above, because a collection tick snapshots the server list
	// at its top: a refresh can land mid-tick and the in-flight tick can
	// re-create a departed server's gauges after this function retired them.
	// When that write is a failed ping it touches no store, so a
	// store-key-driven prune would never notice the server again and the
	// resurrected series would sit on /metrics frozen at 0 forever — which
	// reads to an alert as a hard outage rather than an absent server. See
	// KnownServerSet.DepartedURLs.
	departedURLs := make(map[string]bool)
	for _, serverURL := range app.KnownServers.DepartedURLs(servers) {
		departedURLs[serverURL] = true
	}
	// A removed key whose URL is not configured now is also a departure. For a
	// process that has recorded every config it loaded this adds nothing —
	// DepartedURLs already covers it — but it keeps this function correct on
	// its own terms rather than only in combination with how KnownServers is
	// seeded and recorded elsewhere.
	for key := range removed {
		if serverURL, _, ok := models.ParseServerKey(key); ok && !configuredURLs[serverURL] {
			departedURLs[serverURL] = true
		}
	}
	for serverURL := range departedURLs {
		// Every departed URL is revisited on every refresh, so log only when
		// there was something to retire: otherwise a server removed months ago
		// writes a line a minute for the life of the process.
		if deleted := metrics.DeleteSeriesForServer(serverURL); deleted > 0 {
			app.Logger.Info("Pruned departed server", "server_url", serverURL, "series_deleted", deleted)
		}
	}
	for key := range removed {
		serverURL, agencyID, ok := models.ParseServerKey(key)
		if !ok || departedURLs[serverURL] {
			continue
		}
		if agencyID == "" {
			// The server-scoped key stopped being owned while its URL stayed
			// configured: a server-scoped entry was replaced by agency-scoped
			// ones. Its agency-less series have no writer any more.
			if deleted := metrics.DeleteSeriesForServerScope(serverURL); deleted > 0 {
				app.Logger.Info("Retired server-scoped series after a scope change",
					"server_url", serverURL, "series_deleted", deleted)
			}
			continue
		}
		app.Logger.Info("Pruned departed agency", "server_url", serverURL, "agency_id", agencyID,
			"series_deleted", metrics.DeleteSeriesForAgency(serverURL, agencyID))
	}
}
