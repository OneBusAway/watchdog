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

// In server mode, collectForServerScope logs and reports the error that
// probeReportedAgencies returns. The *url.Error from client.Do repeats the
// raw request URL, including ?key=, so the returned error must be sanitized.
func TestProbeReportedAgenciesNetworkErrorDoesNotLeakAPIKey(t *testing.T) {
	// Start and close a server so its address refuses connections.
	closed := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := closed.URL
	closed.Close()

	app := newTestApplication(t)
	apiKey := "SUPERSECRETOBAKEY"
	server := models.ObaServer{
		ServerName: "probe-network-error",
		ObaBaseURL: strings.Replace(unreachableURL, "://", "://probe-user:probe-pass@", 1),
		ObaApiKey:  apiKey,
	}

	_, _, _, err := app.probeReportedAgencies(context.Background(), server)
	if err == nil {
		t.Fatal("expected error but got none")
	}
	if !strings.Contains(err.Error(), "fetch /metrics.json") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, secret := range []string{apiKey, "probe-user", "probe-pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked credential %q: %v", secret, err)
		}
	}
}
