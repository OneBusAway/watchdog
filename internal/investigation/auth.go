package investigation

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// authenticate wraps next with the bearer-token check.
//
// Until #197 adds the token check it fails closed: every request is rejected
// with 401.
func (s *InvestigationService) authenticate(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		writeProblem(w, http.StatusUnauthorized, "Unauthorized", "A valid bearer token is required.", nil)
	}
}
