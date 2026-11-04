package investigation

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// Register adds every investigation route to router.
func (s *InvestigationService) Register(router *httprouter.Router) {
	s.registerRoutes(router)
}

// registerRoutes holds one s.route line per endpoint.
func (s *InvestigationService) registerRoutes(router *httprouter.Router) {
}

// route registers GET and HEAD for path behind the request pipeline.
// path is the route template, for example /v1/servers/:server_id, and is also
// the metrics route label.
//
// The checks run outermost first: metrics, so rejected requests are counted;
// the token, so a caller without one learns nothing else about the server;
// the database; then content negotiation.
func (s *InvestigationService) route(router *httprouter.Router, path string, handler httprouter.Handle) {
	h := withMetrics(path, s.authenticate(s.requireDatabase(negotiate(handler))))
	router.Handle(http.MethodGet, path, h)
	router.Handle(http.MethodHead, path, h)
}
