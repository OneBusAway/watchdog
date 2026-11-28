package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// Each check reports its own failures to Sentry, and the collector only logs
// the errors they return. These tests run the collector so that a report added
// back on either side shows up as a second event.

// sentryEventsMatching returns the captured events whose message or an
// exception value contains substr.
func sentryEventsMatching(rec *report.RecordingTransport, substr string) []*sentry.Event {
	var out []*sentry.Event
	for _, event := range rec.Events() {
		matched := strings.Contains(event.Message, substr)
		for _, ex := range event.Exception {
			matched = matched || strings.Contains(ex.Value, substr)
		}
		if matched {
			out = append(out, event)
		}
	}
	return out
}

// assertAgencyTags fails the test unless every event carries the agency-scoped
// tags for server.
func assertAgencyTags(t *testing.T, server models.ObaServer, events []*sentry.Event) {
	t.Helper()
	for _, event := range events {
		for tag, want := range map[string]string{
			"agency_id":   server.AgencyID,
			"agency_name": server.AgencyName,
			"server_name": server.ServerName,
		} {
			if got := event.Tags[tag]; got != want {
				t.Errorf("event %q %s tag = %q, want %q", event.Message, tag, got, want)
			}
		}
	}
}

func reportOnceServer(baseURL string) models.ObaServer {
	return models.ObaServer{
		ServerName: "report-once",
		AgencyID:   "agency-once",
		AgencyName: "Agency Once",
		ObaBaseURL: baseURL,
		ObaApiKey:  "test-key",
	}
}

// With no bundle stored and every OBA endpoint returning 404, the bundle check
// and the vehicles-for-agency count each fail. Each failure must produce one
// event, and the bundle event keeps its Warning level.
func TestCollectAgencyChecksReportsEachFailureOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := httptest.NewServer(http.NotFoundHandler())
	defer stub.Close()

	app := newTestApplication(t)
	server := reportOnceServer(stub.URL)

	app.collectAgencyChecks(context.Background(), server, nil, time.Time{}, false)

	bundleEvents := sentryEventsMatching(rec, "there is no bundle")
	if len(bundleEvents) != 1 {
		t.Fatalf("bundle check produced %d Sentry events, want 1", len(bundleEvents))
	}
	bundle := bundleEvents[0]
	if bundle.Level != sentry.LevelWarning {
		t.Errorf("bundle event level = %q, want %q", bundle.Level, sentry.LevelWarning)
	}
	assertAgencyTags(t, server, bundleEvents)

	vehicleEvents := sentryEventsMatching(rec, "vehicles-for-agency")
	if len(vehicleEvents) != 1 {
		t.Fatalf("vehicles-for-agency failure produced %d Sentry events, want 1", len(vehicleEvents))
	}
	assertAgencyTags(t, server, vehicleEvents)
}

// A JSON null from VehiclesForAgency was only reported by the collector, so
// the check has to report it now.
func TestCollectAgencyChecksReportsNullVehiclesResponseOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "vehicles-for-agency") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// #nosec G104
		w.Write([]byte(`null`))
	}))
	defer stub.Close()

	app := newTestApplication(t)
	server := reportOnceServer(stub.URL)
	app.collectAgencyChecks(context.Background(), server, nil, time.Time{}, false)

	events := sentryEventsMatching(rec, "null response")
	if len(events) != 1 {
		t.Fatalf("null VehiclesForAgency response produced %d Sentry events, want 1", len(events))
	}
	assertAgencyTags(t, server, events)
}

// With no GTFS-RT data stored, each of the three vehicle passes fails once,
// so the tick must produce three events, not six.
func TestCollectVehicleMetricsReportsMissingFeedOncePerPass(t *testing.T) {
	rec := report.CaptureSentry(t)
	app := newTestApplication(t)
	server := reportOnceServer("http://oba.example.com")

	app.collectVehicleMetrics(context.Background(), server, nil)

	events := sentryEventsMatching(rec, "no GTFS-RT data available")
	if len(events) != 3 {
		t.Fatalf("missing GTFS-RT feed produced %d Sentry events, want 3 (one per pass)", len(events))
	}
	assertAgencyTags(t, server, events)
}

// A missing bounding box was only reported by the collector, so the
// invalid-vehicles pass has to report it now.
func TestCollectVehicleMetricsReportsMissingBoundingBoxOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	app := newTestApplication(t)
	server := reportOnceServer("http://oba.example.com")
	app.MetricsService.RealtimeStore.Set(server.ServerKey(), &models.RealtimeData{})

	app.collectVehicleMetrics(context.Background(), server, nil)

	events := sentryEventsMatching(rec, "no bounding box found")
	if len(events) != 1 {
		t.Fatalf("missing bounding box produced %d Sentry events, want 1", len(events))
	}
	assertAgencyTags(t, server, events)
}

func pingStub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	stub := httptest.NewServer(handler)
	t.Cleanup(stub.Close)
	return stub
}

// A failed ping is reported by serverPing; the agency-mode gate only logs it.
func TestAgencyEntryAvailableReportsPingFailureOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := pingStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	app := newTestApplication(t)
	if app.agencyEntryAvailable(context.Background(), reportOnceServer(stub.URL)) {
		t.Fatal("expected the failed ping to stop collection")
	}

	if got := len(rec.Events()); got != 1 {
		t.Fatalf("failed ping produced %d Sentry events, want 1", got)
	}
}

// A ping that succeeds but carries no readable time was only reported by the
// collector, so serverPing has to report it now.
func TestAgencyEntryAvailableReportsMissingReadableTimeOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := pingStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// #nosec G104
		w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"readableTime":""}}}`))
	})

	app := newTestApplication(t)
	if app.agencyEntryAvailable(context.Background(), reportOnceServer(stub.URL)) {
		t.Fatal("expected a ping without readable time to stop collection")
	}

	if got := len(sentryEventsMatching(rec, "returned no readable time")); got != 1 {
		t.Fatalf("ping without readable time produced %d matching Sentry events, want 1", got)
	}
	if got := len(rec.Events()); got != 1 {
		t.Fatalf("ping without readable time produced %d Sentry events in all, want 1", got)
	}
}

// The server-mode ping gate only logs too.
func TestServerScopeReportsPingFailureOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := newPingCountingStub(t)
	stub.pingStatus.Store(http.StatusServiceUnavailable)
	app := newTestApplication(t)
	server, scope := serverScopeFixture(t, app, stub.URL)

	app.collectForServerScope(context.Background(), context.Background(), server, scope)

	// A failed ping ends the tick, so every event here comes from the ping.
	if got := len(rec.Events()); got != 1 {
		t.Fatalf("server-mode ping failure produced %d Sentry events, want 1", got)
	}
}
