package investigation

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"
)

// bearerChallenge is the WWW-Authenticate value sent with every 401.
const bearerChallenge = `Bearer realm="watchdog-investigation"`

// authenticate lets a request through to next only when it carries one of the
// accepted bearer tokens. Otherwise it returns 401.
func (s *InvestigationService) authenticate(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		if reason, ok := checkBearer(r.Header.Get("Authorization"), s.tokens); !ok {
			rejectUnauthorized(w, r, s.Logger, reason)
			return
		}
		next(w, r, params)
	}
}

// checkBearer reports whether header is "Bearer <token>" with a token in
// accepted. The scheme is case-insensitive and must be followed by exactly one
// space. When the check fails, reason says why; it never contains the token.
func checkBearer(header string, accepted [][]byte) (reason string, ok bool) {
	if header == "" {
		return "missing Authorization header", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.HasPrefix(token, " ") {
		return "malformed Authorization header", false
	}

	// Compare against every accepted token without stopping early, so the
	// time taken does not reveal which token matched.
	given := []byte(token)
	match := 0
	for _, want := range accepted {
		match |= subtle.ConstantTimeCompare(given, want)
	}
	if match != 1 {
		return "wrong bearer token", false
	}
	return "", true
}

// rejectUnauthorized writes the 401 response. It logs the reason and the path,
// never the header or the token.
func rejectUnauthorized(w http.ResponseWriter, r *http.Request, logger *slog.Logger, reason string) {
	logger.Debug("Investigation API request rejected", "reason", reason, "path", r.URL.Path)
	w.Header().Set("WWW-Authenticate", bearerChallenge)
	writeProblem(w, http.StatusUnauthorized, "Unauthorized", "A valid bearer token is required.", nil)
}
