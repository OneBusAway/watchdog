package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/history"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

// fakeOBAMetricsRecorder stands in for HistoryService and records each call.
type fakeOBAMetricsRecorder struct {
	mu    sync.Mutex
	calls []recordedOBAMetrics
}

type recordedOBAMetrics struct {
	server     models.ObaServer
	agencyIDs  []string
	response   *metrics.OBAMetrics
	observedAt time.Time
}

func (f *fakeOBAMetricsRecorder) RecordOBAMetrics(_ context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, len(agencies))
	for i, agency := range agencies {
		ids[i] = agency.AgencyID
	}
	sort.Strings(ids)
	f.calls = append(f.calls, recordedOBAMetrics{server: server, agencyIDs: ids, response: response, observedAt: observedAt})
}

func (f *fakeOBAMetricsRecorder) recorded() []recordedOBAMetrics {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedOBAMetrics(nil), f.calls...)
}

// agencyModeOBA serves current-time and a metrics.json with the given status
// and body.
func agencyModeOBA(t *testing.T, status int, metricsBody string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "current-time"):
			_, _ = w.Write([]byte(`{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`))
		case r.URL.Path == "/api/where/metrics.json":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(metricsBody))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCollector_NotCalledOnFailure(t *testing.T) {
	ts := agencyModeOBA(t, http.StatusInternalServerError, `{}`)
	app := newTestApplication(t)
	recorder := &fakeOBAMetricsRecorder{}
	app.obaMetricsHistory = recorder

	app.CollectMetricsForServer(context.Background(), models.ObaServer{
		ServerName: "failing",
		AgencyID:   "agency-x",
		AgencyName: "Agency X",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "test-key",
	})

	if calls := recorder.recorded(); len(calls) != 0 {
		t.Fatalf("a failed fetch recorded history %d times, want 0", len(calls))
	}
}

func TestCollector_AgencyModeRecordsResponse(t *testing.T) {
	ts := agencyModeOBA(t, http.StatusOK, `{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-x"],"stopIDsUnmatched":{"agency-x":["s1"]}}}}`)
	app := newTestApplication(t)
	recorder := &fakeOBAMetricsRecorder{}
	app.obaMetricsHistory = recorder
	server := models.ObaServer{
		ServerName: "agency-mode",
		AgencyID:   "agency-x",
		AgencyName: "Agency X",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "test-key",
	}

	before := time.Now().UTC()
	app.CollectMetricsForServer(context.Background(), server)

	calls := recorder.recorded()
	if len(calls) != 1 {
		t.Fatalf("recorded history %d times, want 1", len(calls))
	}
	call := calls[0]
	if len(call.agencyIDs) != 1 || call.agencyIDs[0] != "agency-x" {
		t.Errorf("agencies = %v, want [agency-x]", call.agencyIDs)
	}
	if call.response == nil || len(call.response.Data.Entry.StopIDsUnmatched["agency-x"]) != 1 {
		t.Errorf("recorded response does not carry the fetched stop IDs: %+v", call.response)
	}
	if call.observedAt.Before(before) {
		t.Errorf("observedAt = %v, want the receive time (after %v)", call.observedAt, before)
	}
}

func TestCollector_ServerModeRecordsOncePerTick(t *testing.T) {
	var metricsCalls int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/where/metrics.json" {
			mu.Lock()
			metricsCalls++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"version":2,"data":{"entry":{"agencyIDs":["agency-a","agency-b"]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
	}))
	t.Cleanup(ts.Close)

	app := newTestApplication(t)
	recorder := &fakeOBAMetricsRecorder{}
	app.obaMetricsHistory = recorder
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: ts.URL, ObaApiKey: "test-key"}
	for _, agencyID := range []string{"agency-a", "agency-b"} {
		app.GtfsService.StaticStore.Set(models.ServerKey(ts.URL, agencyID), &models.StaticData{})
	}
	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.ServerScope); !ok {
		t.Fatalf("expected a ServerScope, got %T", scope)
	}

	app.collectForScope(context.Background(), context.Background(), server, scope)

	calls := recorder.recorded()
	if len(calls) != 1 {
		t.Fatalf("recorded history %d times, want once per server per tick", len(calls))
	}
	if got := calls[0].agencyIDs; len(got) != 2 || got[0] != "agency-a" || got[1] != "agency-b" {
		t.Errorf("agencies = %v, want [agency-a agency-b]", got)
	}
	if calls[0].server.ServerName != "multi" || calls[0].observedAt.IsZero() {
		t.Errorf("call = %+v, want the server entry and the probe's receive time", calls[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if metricsCalls != 1 {
		t.Errorf("metrics.json fetched %d times, want 1", metricsCalls)
	}
}

func TestFlushHistory_NoStorageReturnsImmediately(t *testing.T) {
	app := newTestApplication(t)
	app.HistoryService = history.NewHistoryService(nil, app.Logger, "test", nil)
	app.InvestigationDB = nil

	// An in-flight collection cycle holds collectMu.
	app.collectMu.Lock()
	defer app.collectMu.Unlock()

	done := make(chan struct{})
	go func() {
		app.FlushHistory()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FlushHistory waited on collectMu with storage disabled")
	}
}

func TestReconcileHistoryAtStartup_MixedAndAgencyOnlyBaseURLs(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	mixed := models.ObaServer{ObaBaseURL: "https://mixed.example.org", ServerName: "Mixed"}
	mixedB := models.ObaServer{ObaBaseURL: "https://mixed.example.org", ServerName: "Mixed", AgencyID: "b", AgencyName: "Agency b"}
	only := models.ObaServer{ObaBaseURL: "https://only.example.org", ServerName: "Only", AgencyID: "c", AgencyName: "Agency c"}
	removed := models.ObaServer{ObaBaseURL: "https://only.example.org", ServerName: "Only", AgencyID: "d", AgencyName: "Agency d"}

	response := func(agencyIDs []string, unmatched map[string][]string) *metrics.OBAMetrics {
		r := &metrics.OBAMetrics{CurrentTime: t0.UnixMilli(), Version: 2}
		r.Data.Entry.AgencyIDs = agencyIDs
		r.Data.Entry.StopIDsUnmatched = unmatched
		return r
	}

	// Open episodes before the "restart": "a" recorded by the server-scoped
	// entry and "b" by the agency-mode entry on the mixed base URL, "c" and
	// "d" on the agency-only base URL, whose configuration then drops "d".
	hs := history.NewHistoryService(pool, logger, "test", nil)
	hs.RecordOBAMetrics(ctx, mixed, []models.ObaServer{{AgencyID: "a", AgencyName: "Agency a"}}, response([]string{"a"}, map[string][]string{"a": {"1"}}), t0)
	hs.RecordOBAMetrics(ctx, mixedB, []models.ObaServer{mixedB}, response([]string{"b"}, map[string][]string{"b": {"2"}}), t0)
	hs.RecordOBAMetrics(ctx, only, []models.ObaServer{only}, response([]string{"c"}, map[string][]string{"c": {"3"}}), t0)
	hs.RecordOBAMetrics(ctx, removed, []models.ObaServer{removed}, response([]string{"d"}, map[string][]string{"d": {"4"}}), t0)

	// A fresh service over the same database reconciles the startup
	// configuration: the mixed base URL keeps a server-scoped entry plus
	// agency-mode "b", so none of its agencies may close; the agency-only
	// base URL keeps "c", so "d" closes as removed (#438).
	app := newTestApplication(t)
	app.HistoryService = history.NewHistoryService(pool, logger, "test", nil)
	app.ReconcileHistoryAtStartup(ctx, []models.ObaServer{mixed, mixedB, only})

	rows, err := pool.Query(ctx, `
		SELECT s.oba_stop_id, e.close_reason
		FROM unmatched_stop_episode e
		JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	reasonByStop := make(map[string]*string)
	for rows.Next() {
		var stopID string
		var reason *string
		if err := rows.Scan(&stopID, &reason); err != nil {
			t.Fatal(err)
		}
		reasonByStop[stopID] = reason
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(reasonByStop) != 4 {
		t.Fatalf("got %d episodes, want 4", len(reasonByStop))
	}
	for _, stopID := range []string{"1", "2", "3"} {
		if reason := reasonByStop[stopID]; reason != nil {
			t.Errorf("stop %s close_reason = %q, want still open", stopID, *reason)
		}
	}
	if reason := reasonByStop["4"]; reason == nil || *reason != "agency_removed" {
		t.Errorf("removed agency's close_reason = %v, want agency_removed", reason)
	}
}
