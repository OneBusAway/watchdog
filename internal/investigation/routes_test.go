package investigation

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/julienschmidt/httprouter"
)

// okHandler is a stand-in endpoint that records whether it ran.
func okHandler(called *bool) httprouter.Handle {
	return func(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
		*called = true
		w.WriteHeader(http.StatusOK)
	}
}

func TestRoute_FailsClosed(t *testing.T) {
	s, router := newTestService(t)
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	for _, headers := range []map[string]string{
		nil,
		{"Authorization": "Bearer " + testToken},
	} {
		rec := doGet(t, router, "/v1/test", headers)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("headers %v: status = %d, want %d", headers, rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
			t.Errorf("headers %v: Content-Type = %q, want application/problem+json", headers, got)
		}
		var body problem
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Status != http.StatusUnauthorized || body.Title != "Unauthorized" {
			t.Errorf("body = %+v, want status 401 and title Unauthorized", body)
		}
	}
	if called {
		t.Error("handler ran; authenticate must reject every request")
	}
}

func TestRoute_RegistersGetAndHead(t *testing.T) {
	s, router := newTestService(t)
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/test"); h == nil {
			t.Errorf("%s /v1/test is not registered", method)
		}
	}
	if h, _, _ := router.Lookup(http.MethodPost, "/v1/test"); h != nil {
		t.Error("POST /v1/test is registered; only GET and HEAD should be")
	}
}
