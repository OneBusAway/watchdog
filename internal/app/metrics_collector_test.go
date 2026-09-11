package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
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

// Server mode should fetch /api/where/metrics.json once per tick.
// The parsed response is reused by each live agency's
// FetchObaAPIMetrics call.
func TestServerScopeFetchesMetricsOncePerTick(t *testing.T) {
	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")
	var mu sync.Mutex
	metricsCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			w.Header().Set("Content-Type", "application/octet-stream")

			w.Write(rtData)
		case "/api/where/metrics.json":
			mu.Lock()
			metricsCalls++
			n := metricsCalls
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			// Only the first response carries real data. If any agency
			// refetched instead of reading the threaded response, it would see
			if n > 1 {
				w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":[],"realtimeRecordsTotal":{}}}}`))
				return
			}
			w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a","agency-b"],"realtimeRecordsTotal":{"agency-a":1,"agency-b":2}}}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(http.DefaultClient.CloseIdleConnections)

	app := newTestApplication(t)
	baseURL := ts.URL

	server := models.ObaServer{
		ServerName:  "multi",
		ObaBaseURL:  baseURL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: baseURL + "/vehicles.pb"}},
	}

	wholeWorld := geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}
	for _, agencyID := range []string{"agency-a", "agency-b"} {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
	}
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)

	app.GtfsService.RouteAgencyIndex.Set(baseURL, map[string]string{
		"route-a": "agency-a",
		"route-b": "agency-b",
	})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.ServerScope); !ok {
		t.Fatalf("expected a ServerScope for an entry without agency_id, got %T", scope)
	}

	app.collectForScope(context.Background(), server, scope)

	mu.Lock()
	got := metricsCalls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected exactly 1 request to /api/where/metrics.json per tick, got %d", got)
	}
	serverURL := utils.SanitizeServerURL(baseURL)
	for agencyID, want := range map[string]float64{"agency-a": 1, "agency-b": 2} {
		value, found := gaugeValueFor(metrics.ObaRealtimeRecords, map[string]string{
			"agency_id":  agencyID,
			"server_url": serverURL,
		})
		if !found {
			t.Fatalf("no oba_realtime_records_count series for %s; the threaded response was not used", agencyID)
		}
		if value != want {
			t.Fatalf("%s: expected %v from the threaded response, got %v", agencyID, want, value)
		}
	}
}
