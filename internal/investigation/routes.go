package investigation

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// Register adds every investigation route to router.
func (s *InvestigationService) Register(router *httprouter.Router) {
	s.registerRoutes(router)
}

// registerRoutes holds one s.get line per endpoint.
func (s *InvestigationService) registerRoutes(router *httprouter.Router) {
	s.get(router, "/v1/reported-trips", s.listReportedTrips)
	s.get(router, "/v1/reported-trips/:reported_trip_id", s.getReportedTrip)
	s.registerLoader(kindReportedTrip, loadReportedTrips)
	s.get(router, "/v1/cluster-schemes/:scheme_version", s.getClusterScheme)
	s.get(router, "/v1/servers", s.listServers)
	s.get(router, "/v1/servers/:server_id", s.getServer)
	s.get(router, "/v1/unmatched-stop-episodes", s.listUnmatchedStopEpisodes)
	s.get(router, "/v1/unmatched-stop-episodes/:episode_id", s.getUnmatchedStopEpisode)
	s.get(router, "/v1/unmatched-stop-episodes/:episode_id/static-contexts", s.listUnmatchedStopEpisodeStaticContexts)
	s.registerLoader(kindUnmatchedStopStaticContexts, loadStopStaticContexts)
	s.get(router, "/v1/unmatched-trip-episodes/:episode_id", s.getUnmatchedTripEpisode)
	s.get(router, "/v1/unmatched-trip-episodes/:episode_id/static-contexts", s.listUnmatchedTripEpisodeStaticContexts)
	s.registerLoader(kindUnmatchedTripStaticContexts, loadTripStaticContexts)
	s.get(router, "/v1/vehicles", s.listVehicles)
	s.get(router, "/v1/vehicles/:vehicle_entity_id", s.getVehicle)
	s.get(router, "/v1/vehicles/:vehicle_entity_id/anomaly-episodes", s.listVehicleAnomalyEpisodesForVehicle)
	s.registerLoader("vehicle", s.loadVehicles)
	s.get(router, "/v1/vehicle-anomaly-episodes", s.listVehicleAnomalyEpisodes)
	s.get(router, "/v1/vehicle-anomaly-episodes/:episode_id", s.getVehicleAnomalyEpisode)
	s.get(router, "/v1/investigation", s.getInvestigationRoot)
	s.registerLoader(kindServer, s.loadServers)
	s.get(router, "/v1/servers/:server_id/agencies", s.listServerAgencies)
	s.get(router, "/v1/agencies/:agency_id", s.getAgency)
	s.registerLoader(kindAgency, loadAgencies)
	s.get(router, "/v1/servers/:server_id/vehicle-position-endpoints", s.listServerVehiclePositionEndpoints)
	s.get(router, "/v1/episode-counts", s.getEpisodeCounts)
	s.get(router, "/v1/reported-stops", s.listReportedStops)
	s.get(router, "/v1/reported-stops/:reported_stop_id", s.getReportedStop)
	s.get(router, "/v1/reported-stops/:reported_stop_id/unmatched-stop-episodes", s.listReportedStopEpisodes)
	s.registerLoader(kindReportedStop, loadReportedStops)
}

// get registers a read-only endpoint: it serves GET and HEAD requests for
// path, both behind the request pipeline. HEAD gets the same headers as GET,
// and net/http drops its body. Only these two methods are registered, so a
// write endpoint must not use get: it would need its own helper and its own
// pipeline (no read-only transaction, no caching).
//
// path is the route template, for example /v1/servers/:server_id, and is also
// the metrics route label.
//
// The checks run outermost first: metrics, so rejected requests are counted;
// the token, so a caller without one learns nothing else about the server;
// the database; then content negotiation.
func (s *InvestigationService) get(router *httprouter.Router, path string, handler httprouter.Handle) {
	h := withMetrics(path, s.authenticate(s.requireDatabase(negotiate(handler))))
	router.Handle(http.MethodGet, path, h)
	router.Handle(http.MethodHead, path, h)
}
