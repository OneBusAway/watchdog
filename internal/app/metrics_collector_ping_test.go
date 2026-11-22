package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

var pingTestAgencies = []string{"agency-a", "agency-b", "agency-c"}

// pingCountingStub is an OBA stub that counts the requests each test asserts
// on. pingStatus is the HTTP status current-time.json returns.
type pingCountingStub struct {
	*httptest.Server
	pings      atomic.Int32
	vehicles   atomic.Int32
	rtFetches  atomic.Int32
	pingStatus atomic.Int32
}

func newPingCountingStub(t *testing.T) *pingCountingStub {
	t.Helper()
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	stub := &pingCountingStub{}
	stub.pingStatus.Store(http.StatusOK)
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/vehicles.pb":
			stub.rtFetches.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
			return
		case r.URL.Path == "/api/where/current-time.json":
			stub.pings.Add(1)
			if status := int(stub.pingStatus.Load()); status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/where/vehicles-for-agency/"):
			stub.vehicles.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		// #nosec G104
		w.Write([]byte(`{"code":200,"version":2,"data":{"list":[],"entry":{"readableTime":"now","agencyIDs":["agency-a","agency-b","agency-c"]}}}`))
	}))
	t.Cleanup(stub.Close)
	return stub
}

// serverScopeFixture configures a server-scoped entry with pingTestAgencies.
func serverScopeFixture(t *testing.T, app *Application, baseURL string) (models.ObaServer, config.ServerScope) {
	t.Helper()
	server := models.ObaServer{
		ServerName:  "single-ping",
		ObaBaseURL:  baseURL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: baseURL + "/vehicles.pb"}},
	}
	wholeWorld := geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}
	names := make(map[string]string, len(pingTestAgencies))
	for _, agencyID := range pingTestAgencies {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
		names[agencyID] = agencyID
	}
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(baseURL, ""), nil, nil, names)

	scope, ok := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex).(config.ServerScope)
	if !ok {
		t.Fatal("expected ServerScope")
	}
	return server, scope
}

func TestServerScopePingsOncePerCycle(t *testing.T) {
	stub := newPingCountingStub(t)
	app := newTestApplication(t)
	server, scope := serverScopeFixture(t, app, stub.URL)

	for cycle := int32(1); cycle <= 2; cycle++ {
		app.collectForServerScope(context.Background(), context.Background(), server, scope)
		if got := stub.pings.Load(); got != cycle {
			t.Fatalf("after cycle %d: pings = %d, want %d", cycle, got, cycle)
		}
		if got, want := stub.vehicles.Load(), cycle*int32(len(pingTestAgencies)); got != want {
			t.Fatalf("after cycle %d: vehicles-for-agency calls = %d, want %d", cycle, got, want)
		}
		if got := stub.rtFetches.Load(); got != cycle {
			t.Fatalf("after cycle %d: RT fetches = %d, want %d", cycle, got, cycle)
		}
	}
}

// A backoff left under one agency's key must not skip that agency: server mode
// is gated only by the agency-less key.
func TestServerScopeIgnoresAgencyKeyBackoff(t *testing.T) {
	stub := newPingCountingStub(t)
	app := newTestApplication(t)
	server, scope := serverScopeFixture(t, app, stub.URL)
	app.ConfigService.BackoffStore.UpdateBackoff(models.ServerKey(stub.URL, "agency-b"))

	app.collectForServerScope(context.Background(), context.Background(), server, scope)

	if got, want := stub.vehicles.Load(), int32(len(pingTestAgencies)); got != want {
		t.Fatalf("vehicles-for-agency calls = %d, want %d", got, want)
	}
}

func TestServerScopePingFailureGatesAllAgencies(t *testing.T) {
	stub := newPingCountingStub(t)
	stub.pingStatus.Store(http.StatusServiceUnavailable)
	app := newTestApplication(t)
	server, scope := serverScopeFixture(t, app, stub.URL)

	app.collectForServerScope(context.Background(), context.Background(), server, scope)

	if got := stub.pings.Load(); got != 1 {
		t.Fatalf("pings = %d, want 1", got)
	}
	if got := stub.vehicles.Load(); got != 0 {
		t.Fatalf("vehicles-for-agency calls = %d, want 0", got)
	}
	if _, exists := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey()); !exists {
		t.Fatal("failed ping did not grow the server backoff")
	}
	for _, agencyID := range pingTestAgencies {
		if _, exists := app.ConfigService.BackoffStore.NextRetryAt(models.ServerKey(stub.URL, agencyID)); exists {
			t.Fatalf("failed ping wrote backoff for %s", agencyID)
		}
	}
	value, found := gaugeValueFor(metrics.ObaApiStatus, map[string]string{"server_name": server.ServerName, "server_url": stub.URL})
	if !found || value != 0 {
		t.Fatalf("oba_api_status = %v (found %v), want 0", value, found)
	}

	// The next tick is inside the backoff window and must not ping.
	app.collectForServerScope(context.Background(), context.Background(), server, scope)
	if got := stub.pings.Load(); got != 1 {
		t.Fatalf("pings during backoff = %d, want 1", got)
	}
}

func TestAgencyModePingsPerEntry(t *testing.T) {
	stub := newPingCountingStub(t)
	app := newTestApplication(t)
	first := agencyEntry("agency-mode-a", "agency-a", "A", stub.URL, stub.URL+"/vehicles.pb")
	second := agencyEntry("agency-mode-b", "agency-b", "B", stub.URL, stub.URL+"/vehicles.pb")
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{first, second})
	for _, entry := range []models.ObaServer{first, second} {
		app.GtfsService.RouteAgencyIndex.Replace(entry.ServerKey(), nil, nil, map[string]string{entry.AgencyID: entry.AgencyName})
	}

	app.collectOnce(context.Background())
	if got := stub.pings.Load(); got != 2 {
		t.Fatalf("pings = %d, want one per agency entry", got)
	}

	// Backoff stays per entry: only the entry that is not backing off pings.
	app.ConfigService.BackoffStore.UpdateBackoff(first.ServerKey())
	app.collectOnce(context.Background())
	if got := stub.pings.Load(); got != 3 {
		t.Fatalf("pings = %d, want 3 after one entry entered backoff", got)
	}

	// A failed ping grows only that entry's backoff.
	app.ConfigService.BackoffStore.ResetBackoff(first.ServerKey())
	app.ConfigService.BackoffStore.UpdateBackoff(second.ServerKey())
	stub.pingStatus.Store(http.StatusServiceUnavailable)
	app.collectOnce(context.Background())
	if got := stub.pings.Load(); got != 4 {
		t.Fatalf("pings = %d, want 4", got)
	}
	if _, exists := app.ConfigService.BackoffStore.NextRetryAt(first.ServerKey()); !exists {
		t.Fatal("failed agency-mode ping did not grow that entry's backoff")
	}
}
