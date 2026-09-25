package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

func TestMetricsEndpoint(t *testing.T) {
	// Create a new instance of our application
	app := newTestApplication(t)

	// Register the metric without starting the collection routine
	metrics.ObaApiStatus.WithLabelValues("Test Server", "https://test.example.com/current-time.json").Set(1)
	// Create a test server
	ctx := context.Background()
	ts := httptest.NewServer(app.Routes(ctx))
	defer ts.Close()
	// Make a request to the metrics endpoint
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Check status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want %d; got %d", http.StatusOK, resp.StatusCode)
	}
	// Check that the response contains our metric
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), "oba_api_status") {
		t.Error("metrics response doesn't contain oba_api_status metric")
	}
}

func TestCollectMetricsForServer(t *testing.T) {
	app := newTestApplication(t)

	prometheus.DefaultRegisterer = prometheus.NewRegistry()

	testServer := app.ConfigService.Config.Servers[0]

	app.CollectMetricsForServer(context.Background(), testServer)

	getMetricsForTesting(t, metrics.ObaApiStatus)
}

func TestCollectAgencyChecksBackoff(t *testing.T) {
	app := newTestApplication(t)
	testServer := app.ConfigService.Config.Servers[0]

	// Trigger a backoff
	app.ConfigService.BackoffStore.UpdateBackoff(testServer.ServerKey())

	// collectAgencyChecks should return false because it's backing off
	if app.collectAgencyChecks(context.Background(), testServer) {
		t.Fatal("expected collectAgencyChecks to return false during backoff")
	}
}

func TestCollectMetricsForServer_GTFSRTError(t *testing.T) {
	app := newTestApplication(t)
	var rtHits int32
	rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rtHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer rtServer.Close()

	obaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/where/current-time.json":
			w.Write([]byte(`{"code":200,"data":{"entry":{"readableTime":"Test Time"}}}`))
		case "/api/where/metrics.json":
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":["test-agency"]}}}`))
		default:
			w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{}}}`))
		}
	}))
	defer obaServer.Close()

	testServer := app.ConfigService.Config.Servers[0]
	testServer.ObaBaseURL = obaServer.URL
	testServer.GtfsRTFeeds = []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}}

	app.CollectMetricsForServer(context.Background(), testServer)

	if atomic.LoadInt32(&rtHits) == 0 {
		t.Fatal("expected GTFS-RT endpoint to be attempted")
	}
	if app.GtfsService.RealtimeStore.Get(testServer.ServerKey()) != nil {
		t.Fatalf("expected no realtime data after GTFS-RT error")
	}
}

func TestCollectVehicleMetricsIsStandalone(t *testing.T) {
	// collectVehicleMetrics should be safe to invoke independently of the
	// pre-RT steps (server-ping, FetchObaAPIMetrics, etc.). This is the
	// shared helper server-mode calls once per tick, after the RT feed has
	// been fetched for the whole server.
	app := newTestApplication(t)
	testServer := app.ConfigService.Config.Servers[0]

	// No panic, no error path requiring GTFS-RT data we haven't fetched.
	app.collectVehicleMetrics(testServer, nil)
}

// Agency-scoped entries must fetch their own GTFS-RT feed as part of the
// pipeline. Regression test: when the RT fetch was dropped from the
// agency-mode path, nothing populated the realtime store, so every RT-derived
// metric silently errored on every tick with "no GTFS-RT data available".
func TestAgencyScopeFetchesRealtimeFeed(t *testing.T) {
	app := newTestApplication(t)

	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")
	var hits int32
	rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// #nosec G104
		w.Write(rtData)
	}))
	defer rtServer.Close()

	// The pipeline gates on a successful server ping, so stand up a stub OBA
	// server that answers current-time.json (and metrics.json) before the RT
	// step is reached.
	obaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "current-time") {
			// #nosec G104
			w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
			return
		}
		// #nosec G104
		w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["test-agency"]}}}`))
	}))
	defer obaServer.Close()

	server := app.ConfigService.Config.Servers[0]
	server.ObaBaseURL = obaServer.URL
	server.GtfsRTFeeds = []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}}

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.AgencyScope); !ok {
		t.Fatalf("expected an AgencyScope for an entry with agency_id, got %T", scope)
	}

	app.collectForScope(context.Background(), server, scope)

	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("agency-mode collection never fetched the GTFS-RT feed")
	}
	if app.GtfsService.RealtimeStore.Get(server.ServerKey()) == nil {
		t.Fatalf("expected realtime data to be stored under %s", server.ServerKey())
	}
}

func TestStartMetricsCollection(t *testing.T) {
	app := newTestApplication(t)
	// Force a very short ticker to trigger loop quickly
	app.ConfigService.Config.FetchInterval = 1 // 1 second

	ctx, cancel := context.WithCancel(context.Background())
	app.StartMetricsCollection(ctx)

	// Wait a tiny bit then cancel
	cancel()
}

func TestCollectForScope(t *testing.T) {
	app := newTestApplication(t)
	server := app.ConfigService.Config.Servers[0]

	// Server scope error branch
	app.collectForScope(context.Background(), server, config.ServerScope{})
	// Agency scope branch
	app.collectForScope(context.Background(), server, config.AgencyScope{})
	// Invalid scope
	app.collectForScope(context.Background(), server, nil)
}

func TestCollectForServerScope(t *testing.T) {
	app := newTestApplication(t)
	server := app.ConfigService.Config.Servers[0]

	// Live agency
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIds":["a1"]}}}`))
	}))
	defer ts.Close()
	server.ObaBaseURL = ts.URL

	scope := config.ServerScope{
		StaticAgencies: []config.AgencyIdentity{{AgencyID: "a1"}},
	}
	app.collectForServerScope(context.Background(), server, scope)
}

func TestCollectVehicleMetricsError(t *testing.T) {
	app := newTestApplication(t)
	server := app.ConfigService.Config.Servers[0]
	// should not panic if methods error internally due to missing mocked stores
	app.collectVehicleMetrics(server, []models.ObaServer{})
}

func TestBoolToFloat(t *testing.T) {
	if boolToFloat(true) != 1.0 {
		t.Fatal("expected 1.0")
	}
	if boolToFloat(false) != 0.0 {
		t.Fatal("expected 0.0")
	}
}

func TestProbeLiveAgencies_Error(t *testing.T) {
	app := newTestApplication(t)
	server := models.ObaServer{ObaBaseURL: "http://invalid-url\n"} // malformed url
	_, err := app.probeLiveAgencies(context.Background(), server)
	if err == nil {
		t.Fatal("expected error")
	}

	server2 := models.ObaServer{ObaBaseURL: "http://localhost:1"} // connection refused
	_, err = app.probeLiveAgencies(context.Background(), server2)
	if err == nil {
		t.Fatal("expected error")
	}
}
