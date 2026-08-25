package app

import (
	"strings"

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
// feed, route index and last-seen vehicles resident for the life of the
// process, and /metrics keeps advertising it — frozen at whatever value it
// last reported, which reads to an alert as a healthy server rather than an
// absent one.
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
	serverScopedURLs := make(map[string]bool, len(servers))
	agencyKeys := make(map[string]bool, len(servers))
	configuredURLs := make(map[string]bool, len(servers))

	for _, server := range servers {
		serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
		configuredURLs[serverURL] = true
		if strings.TrimSpace(server.AgencyID) == "" {
			serverScopedURLs[serverURL] = true
			continue
		}
		agencyKeys[server.ServerKey()] = true
	}

	keep := func(serverKey string) bool {
		serverURL, _, _ := strings.Cut(serverKey, "|")
		return serverScopedURLs[serverURL] || agencyKeys[serverKey]
	}

	removed := make(map[string]bool)
	for _, keys := range [][]string{
		app.GtfsService.StaticStore.Prune(keep),
		app.GtfsService.RealtimeStore.Prune(keep),
		app.GtfsService.BoundingBoxStore.Prune(keep),
		app.MetricsService.VehicleLastSeen.Prune(keep),
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
	departedURLs := make(map[string]bool)
	for key := range removed {
		if serverURL, _, _ := strings.Cut(key, "|"); !configuredURLs[serverURL] {
			departedURLs[serverURL] = true
		}
	}
	for serverURL := range departedURLs {
		app.Logger.Info("Pruned departed server", "server_url", serverURL,
			"series_deleted", metrics.DeleteSeriesForServer(serverURL))
	}
	for key := range removed {
		serverURL, agencyID, _ := strings.Cut(key, "|")
		if departedURLs[serverURL] || agencyID == "" {
			continue
		}
		app.Logger.Info("Pruned departed agency", "server_url", serverURL, "agency_id", agencyID,
			"series_deleted", metrics.DeleteSeriesForAgency(serverURL, agencyID))
	}
}
