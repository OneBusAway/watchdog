package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
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

func TestAgencyBackoffSentryContextSanitizesObaBaseURL(t *testing.T) {
	rec := report.CaptureSentry(t)
	app := newTestApplication(t)
	server := models.ObaServer{
		ServerName: "backoff-context",
		AgencyID:   "agency-a",
		AgencyName: "Agency A",
		ObaBaseURL: "https://url-user:url-password@oba.example.com/api?token=url-token",
		ObaApiKey:  "api-key-secret",
	}
	app.ConfigService.BackoffStore.UpdateBackoff(server.ServerKey())

	if app.agencyEntryAvailable(context.Background(), server) {
		t.Fatal("agencyEntryAvailable returned true during backoff")
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 report, got %d", len(events))
	}
	event := events[0]
	extra, ok := event.Contexts["extra"]
	if !ok {
		t.Fatalf("expected extra context, got %v", event.Contexts)
	}
	if got, want := extra["oba_base_url"], "https://oba.example.com/api"; got != want {
		t.Errorf("oba_base_url context = %v, want %q", got, want)
	}

	for _, secret := range []string{"url-user", "url-password", "url-token", "api-key-secret"} {
		if strings.Contains(event.Message, secret) || strings.Contains(fmt.Sprint(event.Exception), secret) {
			t.Errorf("report leaked credential %q in message or exception", secret)
		}
		for _, tag := range event.Tags {
			if strings.Contains(tag, secret) {
				t.Errorf("report leaked credential %q in tag", secret)
			}
		}
		for contextName, context := range event.Contexts {
			for key, value := range context {
				if strings.Contains(fmt.Sprint(value), secret) {
					t.Errorf("report leaked credential %q in context %q field %q", secret, contextName, key)
				}
			}
		}
	}
}

func TestServerScopePingLogsDoNotLeakCredentials(t *testing.T) {
	rec := report.CaptureSentry(t)
	stub := newPingCountingStub(t)
	stub.pingStatus.Store(http.StatusServiceUnavailable)
	app := newTestApplication(t)

	var logBuf bytes.Buffer
	app.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const (
		obaUser  = "ping-user"
		obaPass  = "ping-password"
		obaToken = "ping-token"
	)
	rawObaURL := strings.Replace(stub.URL, "://", fmt.Sprintf("://%s:%s@", obaUser, obaPass), 1) + "?token=" + obaToken
	safeObaURL := stub.URL

	server, scope := serverScopeFixture(t, app, stub.URL)
	server.ObaBaseURL = rawObaURL

	// 1. Ping failure path: logs "Skipping server-scope collection due to ping failure"
	app.collectForServerScope(context.Background(), context.Background(), server, scope)

	logOutput := logBuf.String()
	for _, secret := range []string{obaUser, obaPass, obaToken} {
		if strings.Contains(logOutput, secret) {
			t.Errorf("failure log leaked credential %q: %s", secret, logOutput)
		}
	}
	if !strings.Contains(logOutput, safeObaURL) {
		t.Errorf("sanitized URL %q missing from failure log: %s", safeObaURL, logOutput)
	}

	events := rec.Events()
	if len(events) == 0 {
		t.Fatalf("expected at least 1 report, got %d", len(events))
	}
	for i, event := range events {
		if extra, ok := event.Contexts["extra"]; ok {
			if got, exists := extra["oba_base_url"]; exists && got != safeObaURL {
				t.Errorf("event %d oba_base_url context = %v, want %q", i, got, safeObaURL)
			}
		}
		for _, secret := range []string{obaUser, obaPass, obaToken} {
			if strings.Contains(event.Message, secret) || strings.Contains(fmt.Sprint(event.Exception), secret) {
				t.Errorf("report %d leaked credential %q in message or exception", i, secret)
			}
			for _, tag := range event.Tags {
				if strings.Contains(tag, secret) {
					t.Errorf("report %d leaked credential %q in tag", i, secret)
				}
			}
			for contextName, context := range event.Contexts {
				for key, value := range context {
					if strings.Contains(fmt.Sprint(value), secret) {
						t.Errorf("report %d leaked credential %q in context %q field %q", i, secret, contextName, key)
					}
				}
			}
		}
	}

	// 2. Canceled context path: logs "Skipping server-scope collection due to canceled ping"
	logBuf.Reset()
	app.ConfigService.BackoffStore.ResetBackoff(server.ServerKey())
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	app.collectForServerScope(canceledCtx, context.Background(), server, scope)

	logOutput = logBuf.String()
	for _, secret := range []string{obaUser, obaPass, obaToken} {
		if strings.Contains(logOutput, secret) {
			t.Errorf("canceled log leaked credential %q: %s", secret, logOutput)
		}
	}
	if !strings.Contains(logOutput, safeObaURL) {
		t.Errorf("sanitized URL %q missing from canceled log: %s", safeObaURL, logOutput)
	}
}
