package investigation

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/models"
)

// testToken is the bearer token newTestService accepts.
const testToken = "test-token"

// newTestService returns a service with no database that accepts testToken,
// and a router with every investigation route registered.
func newTestService(t *testing.T) (*InvestigationService, *httprouter.Router) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newServiceWithTokens(t, []string{testToken}, logger)
}

// newServiceWithTokens is newTestService with the accepted tokens and the
// logger chosen by the test.
func newServiceWithTokens(t *testing.T, tokens []string, logger *slog.Logger) (*InvestigationService, *httprouter.Router) {
	t.Helper()
	s := NewInvestigationService(nil, func() []models.ObaServer { return nil }, tokens, logger)
	router := httprouter.New()
	s.Register(router)
	return s, router
}

// doGet sends a GET for path to handler with the given headers and returns the
// recorded response.
func doGet(t *testing.T, handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// wantNotFound checks that rec is a 404 problem+json response.
func wantNotFound(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
}

// wantInvalidParam checks that rec is a 400 whose invalid_params names exactly name.
func wantInvalidParam(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var body problem
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.InvalidParams) != 1 || body.InvalidParams[0].Name != name {
		t.Errorf("invalid_params = %+v, want one entry naming %q", body.InvalidParams, name)
	}
}
