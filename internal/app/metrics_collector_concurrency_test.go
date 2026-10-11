package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// stubAgencyServer stands up an httptest OBA server for one agency-scoped
// entry. delay is applied to every OBA/RT response to simulate a slow
// endpoint. agencyID must match server.AgencyID so FetchObaAPIMetrics finds
// it in the metrics.json response.
func stubAgencyServer(t *testing.T, agencyID, agencyName string, delay time.Duration, rtBody []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		switch r.URL.Path {
		case "/vehicles.pb":
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
		default:
			w.Header().Set("Content-Type", "application/json")
			// One body satisfies current-time.json (readableTime),
			// metrics.json (agencyIDs) and vehicles-for-agency (list).
			// #nosec G104
			w.Write([]byte(`{"code":200,"version":2,"data":{"list":[],"entry":{"readableTime":"now","agencyIDs":["` + agencyID + `"]}}}`))
		}
	}))
}

func agencyEntry(serverName, agencyID, agencyName, baseURL, rtURL string) models.ObaServer {
	return models.ObaServer{
		ServerName:      serverName,
		AgencyID:        agencyID,
		AgencyName:      agencyName,
		ObaBaseURL:      baseURL,
		ObaApiKey:       "test-key",
		GtfsRTFeeds:     []models.GtfsRTFeed{{VehiclePositionURL: rtURL}},
		GtfsStaticFeeds: []string{},
	}
}

// TestConcurrentCollectionTimingSlowFirst is the acceptance test for the
// freshness problem: with the slow entry configured first, the healthy entry
// must be done in well under the slow delay.
func TestConcurrentCollectionTimingSlowFirst(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")

	slowStub := stubAgencyServer(t, "slow-agency", "Slow Agency", 600*time.Millisecond, rtBody)
	t.Cleanup(slowStub.Close)
	fastStub := stubAgencyServer(t, "fast-agency", "Fast Agency", 0, rtBody)
	t.Cleanup(fastStub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 30

	slow := agencyEntry("slow-server", "slow-agency", "Slow Agency", slowStub.URL, slowStub.URL+"/vehicles.pb")
	fast := agencyEntry("fast-server", "fast-agency", "Fast Agency", fastStub.URL, fastStub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow, fast})
	for _, s := range []models.ObaServer{slow, fast} {
		app.GtfsService.RouteAgencyIndex.Replace(s.ServerKey(), nil, nil, map[string]string{s.AgencyID: s.AgencyName})
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.collectOnce(context.Background())
	}()

	for app.GtfsService.RealtimeStore.Get(fast.ServerKey()) == nil {
		if time.Since(start) > 10*time.Second {
			t.Fatal("healthy endpoint never completed collection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fastElapsed := time.Since(start)
	<-done

	if fastElapsed >= 1500*time.Millisecond {
		t.Fatalf("healthy endpoint waited for slow endpoint: fast finished after %v (slow per-request delay 600ms x4 steps)", fastElapsed)
	}
}

// TestServerScopeAgencyChecksRunConcurrently verifies the per-agency checks
// fan out (peak concurrency > 1) while server-wide work stays exactly-once.
func TestServerScopeAgencyChecksRunConcurrently(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	var rtCalls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			rtCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
			return
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":["agency-a","agency-b","agency-c"]}}}`))
			return
		default:
			cur := active.Add(1)
			for {
				m := maxActive.Load()
				if cur <= m || maxActive.CompareAndSwap(m, cur) {
					break
				}
			}
			defer active.Add(-1)
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
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
	for _, agencyID := range []string{"agency-a", "agency-b", "agency-c"} {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
	}
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(baseURL, ""), map[string]string{
		"route-a": "agency-a",
		"route-b": "agency-b",
		"route-c": "agency-c",
	}, nil, map[string]string{"agency-a": "a", "agency-b": "b", "agency-c": "c"})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	serverScope, ok := scope.(config.ServerScope)
	if !ok {
		t.Fatalf("expected ServerScope, got %T", scope)
	}

	start := time.Now()
	app.collectForServerScope(context.Background(), server, serverScope)
	elapsed := time.Since(start)

	if got := rtCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 RT fetch, got %d", got)
	}
	if got := maxActive.Load(); got < 2 {
		t.Fatalf("expected concurrent agency checks (peak %d, want >=2)", got)
	}
	// Sequential agency checks would take >= 3 agencies x 300ms x ~2 blocking
	// calls each; concurrent should finish well under that.
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("agency checks look sequential: took %v", elapsed)
	}
	if app.GtfsService.RealtimeStore.Get(server.ServerKey()) == nil {
		t.Fatal("expected merged feed under server-scoped key")
	}
}

// TestCycleDeadlineCancelsStalledWork verifies work exceeding the fetch
// interval is canceled instead of accumulating into later cycles.
func TestCycleDeadlineCancelsStalledWork(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	stub := stubAgencyServer(t, "slow-agency", "Slow Agency", 3*time.Second, rtBody)
	t.Cleanup(stub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 1 // 1s cycle deadline

	slow := agencyEntry("slow-server", "slow-agency", "Slow Agency", stub.URL, stub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow})
	app.GtfsService.RouteAgencyIndex.Replace(slow.ServerKey(), nil, nil, map[string]string{slow.AgencyID: slow.AgencyName})

	start := time.Now()
	app.collectOnce(context.Background())
	elapsed := time.Since(start)

	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("cycle deadline did not cancel stalled work: took %v", elapsed)
	}
}

// TestCollectionCyclesDoNotOverlap verifies a new cycle never overlaps
// unfinished work: while the lock is held, collectOnce must skip without
// issuing any upstream request. A reachable entry is configured deliberately:
// with zero servers the test would pass even if the guard were removed.
func TestCollectionCyclesDoNotOverlap(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	var hits atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/vehicles.pb" {
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// #nosec G104
		w.Write([]byte(`{"code":200,"version":2,"data":{"list":[],"entry":{"readableTime":"now","agencyIDs":["fast-agency"]}}}`))
	}))
	t.Cleanup(stub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 30
	fast := agencyEntry("fast-server", "fast-agency", "Fast Agency", stub.URL, stub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{fast})
	app.GtfsService.RouteAgencyIndex.Replace(fast.ServerKey(), nil, nil, map[string]string{fast.AgencyID: fast.AgencyName})

	app.collectMu.Lock()
	defer app.collectMu.Unlock()

	start := time.Now()
	app.collectOnce(context.Background())
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("overlapping cycle was not skipped: took %v", elapsed)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("overlapping cycle issued %d upstream requests despite held lock", got)
	}
}

// TestCollectionShutdownCancelsActiveWork verifies root-context cancellation
// reaches active workers promptly and collectOnce returns without leaking
// the cycle lock.
func TestCollectionShutdownCancelsActiveWork(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	stub := stubAgencyServer(t, "slow-agency", "Slow Agency", 3*time.Second, rtBody)
	t.Cleanup(stub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 30

	slow := agencyEntry("slow-server", "slow-agency", "Slow Agency", stub.URL, stub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow})
	app.GtfsService.RouteAgencyIndex.Replace(slow.ServerKey(), nil, nil, map[string]string{slow.AgencyID: slow.AgencyName})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		app.collectOnce(ctx)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel active collection")
	}
	if elapsed := time.Since(start); elapsed >= 2500*time.Millisecond {
		t.Fatalf("canceled collection took too long: %v", elapsed)
	}
	// The cycle lock must be free again for the next tick.
	if !app.collectMu.TryLock() {
		t.Fatal("collection lock leaked after cancel")
	}
	app.collectMu.Unlock()

	var _ = runtime.NumGoroutine
}

// TestCollectOnceRotatesAdmissionOrder verifies a shared cycle deadline
// cannot persistently starve later targets: admission starts from a
// round-robin offset that advances every cycle, so an entry skipped when the
// deadline fires is admitted first on a later cycle.
func TestCollectOnceRotatesAdmissionOrder(t *testing.T) {
	origLimit := maxConcurrentCollections
	maxConcurrentCollections = 1
	defer func() { maxConcurrentCollections = origLimit }()

	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	slowStub := stubAgencyServer(t, "slow-agency", "Slow Agency", 3*time.Second, rtBody)
	t.Cleanup(slowStub.Close)
	fastStub := stubAgencyServer(t, "fast-agency", "Fast Agency", 0, rtBody)
	t.Cleanup(fastStub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 1 // 1s shared deadline, 1 slot

	slow := agencyEntry("slow-server", "slow-agency", "Slow Agency", slowStub.URL, slowStub.URL+"/vehicles.pb")
	fast := agencyEntry("fast-server", "fast-agency", "Fast Agency", fastStub.URL, fastStub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow, fast})
	for _, s := range []models.ObaServer{slow, fast} {
		app.GtfsService.RouteAgencyIndex.Replace(s.ServerKey(), nil, nil, map[string]string{s.AgencyID: s.AgencyName})
	}

	// Cycle 1 admits the slow entry first; the deadline fires while it
	// occupies the only slot, so the healthy entry is never admitted.
	app.collectOnce(context.Background())
	if app.GtfsService.RealtimeStore.Get(fast.ServerKey()) != nil {
		t.Fatal("expected healthy entry to miss cycle 1 under a 1s deadline with 1 slot")
	}

	// Cycle 2 must admit the previously starved healthy entry first.
	app.collectOnce(context.Background())
	if app.GtfsService.RealtimeStore.Get(fast.ServerKey()) == nil {
		t.Fatal("rotation did not admit the starved healthy entry on cycle 2")
	}
}

// TestCanceledPingPreservesBackoffAndAvailability verifies a ping canceled by
// the cycle deadline is treated as incomplete collection, not a server
// outage: no backoff is grown and ObaApiStatus is left untouched.
func TestCanceledPingPreservesBackoffAndAvailability(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	stub := stubAgencyServer(t, "slow-agency", "Slow Agency", 3*time.Second, rtBody)
	t.Cleanup(stub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 1 // deadline fires mid-ping

	slow := agencyEntry("cancel-ping-server", "slow-agency", "Slow Agency", stub.URL, stub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow})
	app.GtfsService.RouteAgencyIndex.Replace(slow.ServerKey(), nil, nil, map[string]string{slow.AgencyID: slow.AgencyName})

	app.collectOnce(context.Background())

	if _, exists := app.ConfigService.BackoffStore.NextRetryAt(slow.ServerKey()); exists {
		t.Fatal("canceled ping grew backoff state")
	}
	serverURL := utils.SanitizeServerURL(stub.URL)
	if _, found := gaugeValueFor(metrics.ObaApiStatus, map[string]string{
		"server_name": "cancel-ping-server",
		"server_url":  serverURL,
	}); found {
		t.Fatal("canceled ping overwrote availability gauge")
	}
}

// TestCanceledServerPingPreservesBackoff verifies the server-wide ping in
// server scope is likewise treated as incomplete (not an outage) when the
// context is canceled mid-ping.
func TestCanceledServerPingPreservesBackoff(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// #nosec G104
		w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
	}))
	t.Cleanup(ts.Close)

	app := newTestApplication(t)
	server := models.ObaServer{
		ServerName:  "cancel-ping-multi",
		ObaBaseURL:  ts.URL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL + "/vehicles.pb"}},
	}
	wholeWorld := geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}
	agencyKey := models.ServerKey(ts.URL, "agency-a")
	app.GtfsService.StaticStore.Set(agencyKey, &models.StaticData{})
	app.GtfsService.BoundingBoxStore.Set(agencyKey, wholeWorld)
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(ts.URL, ""), map[string]string{"route-a": "agency-a"}, nil, map[string]string{"agency-a": "a"})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	serverScope, ok := scope.(config.ServerScope)
	if !ok {
		t.Fatalf("expected ServerScope, got %T", scope)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	app.collectForServerScope(ctx, server, serverScope)

	if _, exists := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey()); exists {
		t.Fatal("canceled server ping grew backoff state")
	}
	serverURL := utils.SanitizeServerURL(ts.URL)
	if _, found := gaugeValueFor(metrics.ObaApiStatus, map[string]string{
		"server_name": "cancel-ping-multi",
		"server_url":  serverURL,
	}); found {
		t.Fatal("canceled server ping overwrote availability gauge")
	}
}

// TestCollectOnceRespectsConcurrencyBound verifies the top-level fan-out
// never exceeds maxConcurrentCollections however many entries are
// configured: more entries than slots must queue, not pile on.
func TestCollectOnceRespectsConcurrencyBound(t *testing.T) {
	origLimit := maxConcurrentCollections
	maxConcurrentCollections = 2
	defer func() { maxConcurrentCollections = origLimit }()

	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	var active atomic.Int32
	var maxActive atomic.Int32
	stubFor := func(agencyID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := active.Add(1)
			for {
				m := maxActive.Load()
				if cur <= m || maxActive.CompareAndSwap(m, cur) {
					break
				}
			}
			defer active.Add(-1)
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			if r.URL.Path == "/vehicles.pb" {
				w.Header().Set("Content-Type", "application/octet-stream")
				// #nosec G104
				w.Write(rtBody)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"version":2,"data":{"list":[],"entry":{"readableTime":"now","agencyIDs":["` + agencyID + `"]}}}`))
		}))
	}

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 30

	var servers []models.ObaServer
	for _, agencyID := range []string{"agency-a", "agency-b", "agency-c", "agency-d"} {
		stub := stubFor(agencyID)
		t.Cleanup(stub.Close)
		s := agencyEntry("server-"+agencyID, agencyID, agencyID, stub.URL, stub.URL+"/vehicles.pb")
		servers = append(servers, s)
		app.GtfsService.RouteAgencyIndex.Replace(s.ServerKey(), nil, nil, map[string]string{s.AgencyID: s.AgencyName})
	}
	t.Cleanup(http.DefaultClient.CloseIdleConnections)
	app.ConfigService.Config.UpdateConfig(servers)

	app.collectOnce(context.Background())

	if got := maxActive.Load(); got > 2 {
		t.Fatalf("top-level fan-out exceeded its bound: peak %d, want <=2", got)
	}
	for _, s := range servers {
		if app.GtfsService.RealtimeStore.Get(s.ServerKey()) == nil {
			t.Fatalf("entry %s never collected (bound must queue, not drop)", s.ServerName)
		}
	}
}

// TestServerScopeRespectsAgencyConcurrencyBound verifies the per-agency
// fan-out never exceeds maxConcurrentAgencyChecks while still running
// checks together and fetching the RT feed exactly once.
func TestServerScopeRespectsAgencyConcurrencyBound(t *testing.T) {
	origLimit := maxConcurrentAgencyChecks
	maxConcurrentAgencyChecks = 2
	defer func() { maxConcurrentAgencyChecks = origLimit }()

	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	var rtCalls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			rtCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
			return
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":["agency-a","agency-b","agency-c","agency-d"]}}}`))
			return
		default:
			cur := active.Add(1)
			for {
				m := maxActive.Load()
				if cur <= m || maxActive.CompareAndSwap(m, cur) {
					break
				}
			}
			defer active.Add(-1)
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
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
	for _, agencyID := range []string{"agency-a", "agency-b", "agency-c", "agency-d"} {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
	}
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(baseURL, ""), map[string]string{
		"route-a": "agency-a",
		"route-b": "agency-b",
		"route-c": "agency-c",
		"route-d": "agency-d",
	}, nil, map[string]string{"agency-a": "a", "agency-b": "b", "agency-c": "c", "agency-d": "d"})

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	serverScope, ok := scope.(config.ServerScope)
	if !ok {
		t.Fatalf("expected ServerScope, got %T", scope)
	}

	app.collectForServerScope(context.Background(), server, serverScope)

	if got := rtCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 RT fetch, got %d", got)
	}
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("per-agency fan-out exceeded its bound: peak %d, want <=2", got)
	}
	if got := maxActive.Load(); got < 2 {
		t.Fatalf("expected concurrent agency checks (peak %d, want >=2)", got)
	}
}

// TestCanceledCycleLeavesCompletionGaugeUnchanged verifies the freshness
// rule: a cycle cut short by its deadline must not advance
// WatchdogCollectionLastCompleted, while a successful cycle does.
func TestCanceledCycleLeavesCompletionGaugeUnchanged(t *testing.T) {
	const seed = 12345.0
	metrics.WatchdogCollectionLastCompleted.Set(seed)

	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	slowStub := stubAgencyServer(t, "slow-agency", "Slow Agency", 3*time.Second, rtBody)
	t.Cleanup(slowStub.Close)
	fastStub := stubAgencyServer(t, "fast-agency", "Fast Agency", 0, rtBody)
	t.Cleanup(fastStub.Close)

	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 1 // deadline fires mid-cycle

	slow := agencyEntry("slow-server", "slow-agency", "Slow Agency", slowStub.URL, slowStub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{slow})
	app.GtfsService.RouteAgencyIndex.Replace(slow.ServerKey(), nil, nil, map[string]string{slow.AgencyID: slow.AgencyName})

	app.collectOnce(context.Background())
	if got := completionGaugeValue(t); got != seed {
		t.Fatalf("canceled cycle moved completion gauge: got %v, want %v", got, seed)
	}

	// A healthy cycle must still advance the gauge past the seed.
	app.ConfigService.Config.FetchInterval = 30
	fast := agencyEntry("fast-server", "fast-agency", "Fast Agency", fastStub.URL, fastStub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{fast})
	app.GtfsService.RouteAgencyIndex.Replace(fast.ServerKey(), nil, nil, map[string]string{fast.AgencyID: fast.AgencyName})

	app.collectOnce(context.Background())
	if got := completionGaugeValue(t); got <= seed {
		t.Fatalf("successful cycle did not advance completion gauge: got %v, want > %v", got, seed)
	}
}

func completionGaugeValue(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := metrics.WatchdogCollectionLastCompleted.Write(m); err != nil {
		t.Fatalf("failed to read completion gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}
