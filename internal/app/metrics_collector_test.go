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
	"watchdog.onebusaway.org/internal/gtfs"
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

// A freshness timestamp should not be emitted when no GTFS-RT feeds are configured.
func TestAgencyScopeGtfsRtFreshnessNotSetWithoutFeeds(t *testing.T) {
	app := newTestApplication(t)

	obaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "current-time") {
			_, _ = w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":[]}}}`))
	}))
	defer obaServer.Close()

	server := models.ObaServer{
		ServerName: "empty-feeds",
		AgencyName: "Empty Feeds",
		AgencyID:   "agency-empty-feeds",
		ObaBaseURL: obaServer.URL,
		ObaApiKey:  "test-key",
	}

	app.CollectMetricsForServer(context.Background(), server)

	serverURL := utils.SanitizeServerURL(obaServer.URL)
	if got := seriesCount(metrics.GtfsRtLastSuccessfulFetch, prometheus.Labels{
		"agency_id":  server.AgencyID,
		"server_url": serverURL,
	}); got != 0 {
		t.Fatalf("expected no GTFS-RT freshness series for a server with no feeds, got %d", got)
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
	// A scoped RT fetch requires an attribution snapshot. This empty snapshot is
	// enough to verify that an empty filtered result is still published.
	app.GtfsService.RouteAgencyIndex.Replace(server.ServerKey(), nil, nil, map[string]string{"test-agency": "Test Agency"})

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

// Agency-scoped freshness must be tracked independently when multiple
// agencies share the same OBA server URL.
func TestAgencyScopeGtfsRtFreshnessIsPerAgency(t *testing.T) {
	app := newTestApplication(t)

	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")
	rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(rtData)
	}))
	defer rtServer.Close()

	obaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "current-time") {
			_, _ = w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
			return
		}

		_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":[]}}}`))
	}))
	defer obaServer.Close()

	servers := []models.ObaServer{
		{
			ServerName: "multi",
			AgencyName: "Agency A",
			AgencyID:   "agency-a",
			ObaBaseURL: obaServer.URL,
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: rtServer.URL},
			},
		},
		{
			ServerName: "multi",
			AgencyName: "Agency B",
			AgencyID:   "agency-b",
			ObaBaseURL: obaServer.URL,
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: rtServer.URL},
			},
		},
	}

	for _, server := range servers {
		app.CollectMetricsForServer(context.Background(), server)
	}

	serverURL := utils.SanitizeServerURL(obaServer.URL)

	if got := seriesCount(metrics.GtfsRtLastSuccessfulFetch, prometheus.Labels{
		"server_url": serverURL,
	}); got != 2 {
		t.Fatalf("expected 2 GTFS-RT freshness series, got %d", got)
	}

	for _, agency := range servers {
		if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, map[string]string{
			"agency_id":   agency.AgencyID,
			"agency_name": agency.AgencyName,
			"server_name": agency.ServerName,
			"server_url":  serverURL,
		}); !found {
			t.Fatalf("missing GTFS-RT freshness series for %s", agency.AgencyID)
		}
	}
}

// A successful fetch for one agency must not refresh freshness for another
// agency sharing the same OBA server URL.
func TestAgencyScopeGtfsRtFreshnessNotRefreshedByAnotherAgency(t *testing.T) {
	app := newTestApplication(t)

	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")

	var rtCalls atomic.Int32
	rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rtCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rtData)
			return
		}

		http.Error(w, "GTFS-RT fetch failed", http.StatusInternalServerError)
	}))
	defer rtServer.Close()

	obaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "current-time") {
			_, _ = w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
			return
		}

		_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":[]}}}`))
	}))
	defer obaServer.Close()

	servers := []models.ObaServer{
		{
			ServerName: "multi",
			AgencyName: "Agency A",
			AgencyID:   "agency-a",
			ObaBaseURL: obaServer.URL,
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: rtServer.URL},
			},
		},
		{
			ServerName: "multi",
			AgencyName: "Agency B",
			AgencyID:   "agency-b",
			ObaBaseURL: obaServer.URL,
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: rtServer.URL},
			},
		},
	}

	for _, server := range servers {
		app.CollectMetricsForServer(context.Background(), server)
	}

	serverURL := utils.SanitizeServerURL(obaServer.URL)

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, map[string]string{
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  serverURL,
	}); !found {
		t.Fatal("expected freshness series for successfully fetched agency")
	}

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, map[string]string{
		"agency_id":   "agency-b",
		"agency_name": "Agency B",
		"server_name": "multi",
		"server_url":  serverURL,
	}); found {
		t.Fatal("expected no freshness series for failed agency fetch")
	}
}

// Server mode should fetch /api/where/metrics.json once per tick.
// The parsed response is reused by each OBA-reported agency's
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
			// an empty agencyIDs list and record nothing, failing the gauge
			// assertions below.
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

	app.GtfsService.RouteAgencyIndex.Replace(server.ServerKey(), map[string]string{
		"route-a": "agency-a",
		"route-b": "agency-b",
	}, nil, nil)

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

func TestServerScopeGtfsRtFreshnessNotSetWithoutFeeds(t *testing.T) {
	app := newTestApplication(t)

	obasServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "metrics.json") {
			_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a"]}}}`))
			return
		}
		if strings.Contains(r.URL.Path, "current-time") {
			_, _ = w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a"]}}}`))
	}))
	defer obasServer.Close()

	server := models.ObaServer{
		ServerName: "empty-feeds",
		ObaBaseURL: obasServer.URL,
		ObaApiKey:  "test-key",
	}

	key := models.ServerKey(server.ObaBaseURL, "agency-a")

	app.GtfsService.StaticStore.Set(key, &models.StaticData{})
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(server.ObaBaseURL, ""), nil, nil, map[string]string{"agency-a": "Agency A"})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.ServerScope); !ok {
		t.Fatalf("expected a ServerScope for an entry without agency_id, got %T", scope)
	}

	app.collectForScope(context.Background(), server, scope)

	serverURL := utils.SanitizeServerURL(obasServer.URL)

	if got := seriesCount(metrics.GtfsRtLastSuccessfulFetch, prometheus.Labels{
		"agency_id":  "",
		"server_url": serverURL,
	}); got != 0 {
		t.Fatalf("expected no GTFS-RT freshness series for a server with no feeds, got %d", got)
	}
}

// Removing all GTFS-RT feeds should retire the existing freshness series.
func TestAgencyScopeGtfsRtFreshnessRemovedWhenFeedsRemoved(t *testing.T) {
	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")
	obasServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(rtData)
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a"]}}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(obasServer.Close)
	t.Cleanup(http.DefaultClient.CloseIdleConnections)

	app := newTestApplication(t)

	server := models.ObaServer{
		ServerName:  "agency-a",
		AgencyID:    "agency-a",
		AgencyName:  "Agency A",
		ObaBaseURL:  obasServer.URL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: obasServer.URL + "/vehicles.pb"}},
	}

	key := server.ServerKey()
	app.GtfsService.StaticStore.Set(key, &models.StaticData{})
	app.GtfsService.BoundingBoxStore.Set(key, geo.BoundingBox{
		MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180,
	})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.AgencyScope); !ok {
		t.Fatalf("expected an AgencyScope, got %T", scope)
	}

	app.collectForScope(context.Background(), server, scope)

	serverURL := utils.SanitizeServerURL(obasServer.URL)
	labels := map[string]string{
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "agency-a",
		"server_url":  serverURL,
	}

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, labels); !found {
		t.Fatal("expected freshness series after successful GTFS-RT fetch")
	}

	server.GtfsRTFeeds = nil
	app.collectForScope(context.Background(), server, scope)

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, labels); found {
		t.Fatal("expected freshness series to be removed after GTFS-RT feeds were removed")
	}
}

// Removing all GTFS-RT feeds should retire the existing server-scoped freshness series.
func TestServerScopeGtfsRtFreshnessRemovedWhenFeedsRemoved(t *testing.T) {
	rtData := readTestFixture(t, "../../testdata/gtfs_rt_feed_vehicles.pb")
	obasServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(rtData)
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a"]}}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(obasServer.Close)
	t.Cleanup(http.DefaultClient.CloseIdleConnections)

	app := newTestApplication(t)

	server := models.ObaServer{
		ServerName:  "server-scope",
		ObaBaseURL:  obasServer.URL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: obasServer.URL + "/vehicles.pb"}},
	}

	wholeWorld := geo.BoundingBox{
		MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180,
	}
	key := models.ServerKey(obasServer.URL, "agency-a")
	app.GtfsService.StaticStore.Set(key, &models.StaticData{})
	app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(server.ServerKey(), map[string]string{
		"route-a": "agency-a",
	}, nil, nil)

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.ServerScope); !ok {
		t.Fatalf("expected a ServerScope, got %T", scope)
	}

	app.collectForScope(context.Background(), server, scope)

	serverURL := utils.SanitizeServerURL(obasServer.URL)
	labels := map[string]string{
		"agency_id":   "",
		"agency_name": "",
		"server_name": "server-scope",
		"server_url":  serverURL,
	}

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, labels); !found {
		t.Fatal("expected freshness series after successful GTFS-RT fetch")
	}

	server.GtfsRTFeeds = nil
	app.collectForScope(context.Background(), server, scope)

	if _, found := gaugeValueFor(metrics.GtfsRtLastSuccessfulFetch, labels); found {
		t.Fatal("expected freshness series to be removed after GTFS-RT feeds were removed")
	}
}

func TestServerScopeRetiresVehicleStateWhenNoAgencyIsReported(t *testing.T) {
	var rtCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/vehicles.pb":
			rtCalls.Add(1)
		case "/api/where/metrics.json":
			_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":[],"realtimeRecordsTotal":{}}}}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(ts.Close)

	app := newTestApplication(t)
	server := models.ObaServer{
		ServerName: "multi", ObaBaseURL: ts.URL,
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL + "/vehicles.pb"}},
	}
	agency := serverForAgency(server, "agency-a", "Agency A")
	app.GtfsService.StaticStore.Set(agency.ServerKey(), &models.StaticData{})
	app.GtfsService.RouteAgencyIndex.Replace(server.ServerKey(), map[string]string{"route-a": agency.AgencyID}, nil, map[string]string{agency.AgencyID: agency.AgencyName})

	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	metrics.RealtimeVehiclePositions.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(4)
	metrics.TrackedVehiclesGauge.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(4)
	metrics.AgencyActiveVehiclesGauge.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(4)
	metrics.ObaVehiclesLastSuccessfulFetch.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(123)
	metrics.GtfsRtUnattributedVehicles.WithLabelValues(server.ServerName, serverURL).Set(2)
	metrics.GtfsRtUnattributedVehiclesByReason.WithLabelValues(server.ServerName, serverURL, string(gtfs.AttributionAmbiguousIdentifier)).Set(2)
	metrics.GtfsRtUnattributedVehicleCandidateAssociations.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(2)
	app.MetricsService.VehicleLastSeen.Set(agency.ServerKey(), "0", "vehicle-a", metrics.LastSeen{
		VehicleID: "vehicle-a", FeedID: "0", AgencyID: agency.AgencyID, AgencyName: agency.AgencyName,
		ServerName: server.ServerName, ServerURL: serverURL,
	})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	app.collectForScope(context.Background(), server, scope)

	if rtCalls.Load() != 0 {
		t.Fatalf("GTFS-RT fetched with no reported agencies: %d", rtCalls.Load())
	}
	if got := app.MetricsService.VehicleLastSeen.Count(agency.ServerKey()); got != 0 {
		t.Fatalf("inactive agency retained %d vehicles", got)
	}
	for _, vec := range []*prometheus.GaugeVec{metrics.RealtimeVehiclePositions, metrics.TrackedVehiclesGauge} {
		if value, found := gaugeValueFor(vec, map[string]string{"agency_id": agency.AgencyID, "server_url": serverURL}); !found || value != 0 {
			t.Fatalf("inactive agency gauge = %v, found=%t", value, found)
		}
	}
	for _, vec := range []prometheus.Collector{metrics.AgencyActiveVehiclesGauge, metrics.ObaVehiclesLastSuccessfulFetch} {
		if got := seriesCount(vec, prometheus.Labels{"agency_id": agency.AgencyID, "server_url": serverURL}); got != 0 {
			t.Fatalf("inactive agency retained stale OBA vehicle series: %d", got)
		}
	}
	for _, vec := range []prometheus.Collector{
		metrics.GtfsRtUnattributedVehicles,
		metrics.GtfsRtUnattributedVehiclesByReason,
		metrics.GtfsRtUnattributedVehicleCandidateAssociations,
	} {
		if got := seriesCount(vec, prometheus.Labels{"server_url": serverURL}); got != 0 {
			t.Fatalf("stale attribution diagnostics survived: %d", got)
		}
	}
}

func TestServerScopePreservesVehicleStateWhenCoverageProbeFails(t *testing.T) {
	var rtCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/vehicles.pb":
			rtCalls.Add(1)
		case "/api/where/metrics.json":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(ts.Close)

	app := newTestApplication(t)
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: ts.URL, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL + "/vehicles.pb"}}}
	agency := serverForAgency(server, "agency-a", "Agency A")
	app.GtfsService.StaticStore.Set(agency.ServerKey(), &models.StaticData{})
	app.GtfsService.RouteAgencyIndex.Replace(server.ServerKey(), map[string]string{"route-a": agency.AgencyID}, nil, map[string]string{agency.AgencyID: agency.AgencyName})
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	metrics.RealtimeVehiclePositions.WithLabelValues(agency.AgencyID, agency.AgencyName, server.ServerName, serverURL).Set(4)
	app.MetricsService.VehicleLastSeen.Set(agency.ServerKey(), "0", "vehicle-a", metrics.LastSeen{VehicleID: "vehicle-a", FeedID: "0"})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	app.collectForScope(context.Background(), server, scope)

	if rtCalls.Load() != 0 {
		t.Fatalf("GTFS-RT fetched after failed coverage probe: %d", rtCalls.Load())
	}
	if got := app.MetricsService.VehicleLastSeen.Count(agency.ServerKey()); got != 1 {
		t.Fatalf("failed coverage probe retired vehicle state: %d", got)
	}
	if value, found := gaugeValueFor(metrics.RealtimeVehiclePositions, map[string]string{"agency_id": agency.AgencyID, "server_url": serverURL}); !found || value != 4 {
		t.Fatalf("failed coverage probe changed vehicle gauge: %v, found=%t", value, found)
	}
}
