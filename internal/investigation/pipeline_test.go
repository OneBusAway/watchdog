package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/metrics"
)

// counterValue reads one counter series through the client model.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// histogramCount reads how many observations one histogram series has.
func histogramCount(t *testing.T, o prometheus.Observer) uint64 {
	t.Helper()
	var m dto.Metric
	if err := o.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("read histogram: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

// serve runs h for a GET of path with the given headers and returns the
// recorded response.
func serve(t *testing.T, h httprouter.Handle, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	router := httprouter.New()
	router.Handle(http.MethodGet, "/v1/test", h)
	return doGet(t, router, "/v1/test", headers)
}

// decodeProblem decodes a problem+json body and checks its content type.
func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problem {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var body problem
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestRoute_DatabaseDisabled(t *testing.T) {
	s, _ := newTestService(t) // no pool
	called := false

	rec := serve(t, s.requireDatabase(okHandler(&called)), nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if body := decodeProblem(t, rec); body.Title != "Investigation storage disabled" {
		t.Errorf("title = %q, want %q", body.Title, "Investigation storage disabled")
	}
	if called {
		t.Error("handler ran without a database")
	}
}

func TestRoute_NotAcceptable(t *testing.T) {
	cases := []struct {
		accept string // "" means no Accept header
		want   int
	}{
		{"", http.StatusOK},
		{"*/*", http.StatusOK},
		{"application/json", http.StatusOK},
		{"application/*", http.StatusOK},
		{"text/html, application/json;q=0.5", http.StatusOK},
		{"text/html", http.StatusNotAcceptable},
		{"application/json;q=0", http.StatusNotAcceptable},
		{"application/json;q=0, text/html", http.StatusNotAcceptable},
		{"not a media type", http.StatusNotAcceptable},
	}
	for _, tc := range cases {
		t.Run(tc.accept, func(t *testing.T) {
			called := false
			var headers map[string]string
			if tc.accept != "" {
				headers = map[string]string{"Accept": tc.accept}
			}

			rec := serve(t, negotiate(okHandler(&called)), headers)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusNotAcceptable {
				decodeProblem(t, rec)
				if called {
					t.Error("handler ran for an unacceptable request")
				}
			}
		})
	}
}

func TestRoute_Metrics(t *testing.T) {
	const route = "/v1/metrics-test/:id"
	s, router := newTestService(t)
	called := false
	s.route(router, route, okHandler(&called))

	requests := metrics.InvestigationAPIRequests.WithLabelValues(route, "401")
	before := counterValue(t, requests)
	durations := metrics.InvestigationAPIRequestDuration.WithLabelValues(route)
	durationsBefore := histogramCount(t, durations)

	doGet(t, router, "/v1/metrics-test/42", nil)

	if got := counterValue(t, requests) - before; got != 1 {
		t.Errorf("requests counted under %s = %v, want 1", route, got)
	}
	concrete := metrics.InvestigationAPIRequests.WithLabelValues("/v1/metrics-test/42", "401")
	if got := counterValue(t, concrete); got != 0 {
		t.Errorf("requests counted under the concrete path = %v, want 0", got)
	}
	if got := histogramCount(t, durations) - durationsBefore; got != 1 {
		t.Errorf("durations observed under %s = %d, want 1", route, got)
	}
}

func TestWithMetrics_DefaultsToOK(t *testing.T) {
	const route = "/v1/metrics-default-test"
	silent := func(http.ResponseWriter, *http.Request, httprouter.Params) {}
	requests := metrics.InvestigationAPIRequests.WithLabelValues(route, "200")
	before := counterValue(t, requests)

	serve(t, withMetrics(route, silent), nil)

	if got := counterValue(t, requests) - before; got != 1 {
		t.Errorf("requests counted with status 200 = %v, want 1", got)
	}
}

func TestRoute_TokenCheckedBeforeDatabase(t *testing.T) {
	s, router := newTestService(t) // no pool, and no token check until #197
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	rec := doGet(t, router, "/v1/test", map[string]string{"Accept": "text/html"})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d: the token check must run before the database and Accept checks", rec.Code, http.StatusUnauthorized)
	}
}
