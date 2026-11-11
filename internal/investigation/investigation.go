package investigation

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// investigationRoot is the E01 body: the API root document, with links to
// every top-level collection. The body is constant; it does not read the
// database, so this endpoint does not require investigation storage to be
// enabled.
type investigationRoot struct {
	APIVersion string `json:"api_version"`
	Links      links  `json:"links"`
}

// getInvestigationRoot serves GET /v1/investigation (E01): the API root
// document. It lists every top-level collection so a client can start from
// one known address and follow links from there, without hard-coding URLs.
func (s *InvestigationService) getInvestigationRoot(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad) // no allowed parameters
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	body := investigationRoot{
		APIVersion: "v1",
		Links: links{
			"self":                     "/v1/investigation",
			"servers":                  "/v1/servers",
			"unmatched_stop_episodes":  "/v1/unmatched-stop-episodes",
			"unmatched_trip_episodes":  "/v1/unmatched-trip-episodes",
			"vehicle_anomaly_episodes": "/v1/vehicle-anomaly-episodes",
			"reported_stops":           "/v1/reported-stops",
			"reported_trips":           "/v1/reported-trips",
			"vehicles":                 "/v1/vehicles",
			"gtfs_static_versions":     "/v1/gtfs-static-versions",
			"vehicle_anomaly_types":    "/v1/vehicle-anomaly-types",
			"cluster_schemes":          "/v1/cluster-schemes",
			"unmatched_stop_clusters":  "/v1/unmatched-stop-clusters",
			"episode_counts":           "/v1/episode-counts",
		},
	}
	s.writeJSON(w, r, body, cacheShort())
}
