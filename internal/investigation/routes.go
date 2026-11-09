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
// path is the route template, for example /v1/servers/:server_id.
func (s *InvestigationService) route(router *httprouter.Router, path string, handler httprouter.Handle) {
	h := s.authenticate(handler)
	router.Handle(http.MethodGet, path, h)
	router.Handle(http.MethodHead, path, h)
}
