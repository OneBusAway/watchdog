package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// A server without /api/where/metrics.json must produce one Sentry event per
// tick for that failure, not two: fetchObaAPIMetrics reports it, and the
// collector only logs it.
func TestCollectAgencyChecksReportsMetricsAPIFailureOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer stub.Close()

	app := newTestApplication(t)
	server := models.ObaServer{
		ServerName: "metrics-404",
		AgencyID:   "agency-404",
		AgencyName: "Agency 404",
		ObaBaseURL: stub.URL,
		ObaApiKey:  "test-key",
	}

	app.collectAgencyChecks(context.Background(), server, nil, time.Time{}, false)

	// collectAgencyChecks also runs other per-agency checks that can report
	// their own failures here, so count only the metrics API events.
	var metricsEvents int
	for _, event := range rec.Events() {
		if !strings.Contains(event.Message+fmt.Sprint(event.Exception), "does not support metrics API") {
			continue
		}
		metricsEvents++
		if got := event.Tags["agency_name"]; got != server.AgencyName {
			t.Errorf("agency_name tag = %q, want %q", got, server.AgencyName)
		}
	}
	if metricsEvents != 1 {
		t.Fatalf("metrics API 404 produced %d Sentry events, want 1", metricsEvents)
	}
}
