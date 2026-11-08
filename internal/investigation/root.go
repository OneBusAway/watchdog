package investigation

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// investigationRootBody is the E01 body: the API version and a link to every
// top-level collection, so a client can start from one address and follow
// links from there.
type investigationRootBody struct {
	APIVersion string `json:"api_version"`
	Links      links  `json:"links"`
}

// getInvestigationRoot serves GET /v1/investigation. The body is constant and
// reads nothing from the database. It takes no query parameters.
func (s *InvestigationService) getInvestigationRoot(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}
	s.writeJSON(w, r, investigationRoot(), cacheShort())
}

// investigationRoot returns the E01 body. Some collections need query
// parameters, which the client adds as each endpoint documents.
func investigationRoot() investigationRootBody {
	return investigationRootBody{
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
}
