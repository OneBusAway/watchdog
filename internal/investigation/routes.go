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
	s.get(router, "/v1/servers", s.listServers)
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
