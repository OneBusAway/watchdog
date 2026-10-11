package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

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
// unfinished work: while the lock is held, collectOnce skips immediately.
func TestCollectionCyclesDoNotOverlap(t *testing.T) {
	app := newTestApplication(t)
	app.ConfigService.Config.FetchInterval = 30
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{})

	app.collectMu.Lock()
	defer app.collectMu.Unlock()

	start := time.Now()
	app.collectOnce(context.Background())
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("overlapping cycle was not skipped: took %v", elapsed)
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
