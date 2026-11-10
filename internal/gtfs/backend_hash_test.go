package gtfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestBackendHashRefresh(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	var campaignLogs bytes.Buffer
	h.service.Logger = slog.New(slog.NewJSONHandler(&campaignLogs, nil))
	var hash atomic.Value
	var downloads atomic.Int32
	hash.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/metadata.json" {
			if r.URL.Query().Get("key") != "test-key" {
				t.Error("missing API key")
			}
			fmt.Fprintf(w, `{"staticGtfsHash":%q}`, hash.Load().(string))
		} else {
			downloads.Add(1)
			w.Write(h.body.Load().([]byte))
		}
	}))
	defer ts.Close()
	h.server.ObaBaseURL = ts.URL
	h.server.ObaApiKey = "test-key"
	h.server.GtfsStaticFeeds = []string{ts.URL + "/feed.zip"}
	h.store.SetConfiguredServers([]models.ObaServer{h.server})
	h.run(t, h.server, StaticRefreshStartup)
	sum := sha256.Sum256(h.body.Load().([]byte))
	localHash := hex.EncodeToString(sum[:])
	var observed *bool
	h.service.BackendHashObserver = func(_ models.ObaServer, match *bool) { observed = match }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check := func() { h.service.CheckBackendBundles(ctx, ctx, []models.ObaServer{h.server}) }
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			h.service.refreshCoordinator.mu.Lock()
			n := len(h.service.refreshCoordinator.active)
			h.service.refreshCoordinator.mu.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("refresh did not finish")
	}
	hash.Store(localHash)
	check()
	if observed == nil || !*observed || downloads.Load() != 1 {
		t.Fatal("matching hash triggered refresh or reported mismatch")
	}
	hash.Store(strings.Repeat("a", 64))
	// A startup/daily campaign already in flight must not consume this hash's attempt.
	c := h.service.refreshCoordinator
	c.active[h.server.ServerKey()] = activeStaticCampaign{fingerprint: staticConfigFingerprint(h.server), cancel: func() {}}
	check()
	if len(c.backendAttempts) != 0 || downloads.Load() != 1 {
		t.Fatal("active campaign consumed backend transition")
	}
	delete(c.active, h.server.ServerKey())
	check()
	wait()
	check()
	if observed == nil || *observed || downloads.Load() != 2 {
		t.Fatalf("persistent mismatch: observed=%v downloads=%d", observed, downloads.Load())
	}
	// A second backend version gets another attempt, even after an unresolved mismatch.
	hash.Store(strings.Repeat("b", 64))
	check()
	wait()
	if downloads.Load() != 3 {
		t.Fatal("new backend hash did not trigger refresh")
	}
	// Load-balanced nodes may alternate versions after campaigns finish.
	for _, version := range []string{"a", "b", "a", "b"} {
		hash.Store(strings.Repeat(version, 64))
		check()
		wait()
		if downloads.Load() != 3 {
			t.Fatal("alternating backend hashes downloaded again")
		}
	}
	// A daily refresh remains independent of backend attempt suppression.
	h.body.Store(withExtraZipEntry(t, h.body.Load().([]byte)))
	sum = sha256.Sum256(h.body.Load().([]byte))
	hash.Store(hex.EncodeToString(sum[:]))
	check()
	wait()
	check()
	if observed == nil || !*observed {
		t.Fatal("successful refresh did not resolve mismatch")
	}
	h.run(t, h.server, StaticRefreshDaily)
	if downloads.Load() != 5 {
		t.Fatalf("daily refresh did not run: %d", downloads.Load())
	}
	if got := strings.Count(campaignLogs.String(), `"trigger":"backend_changed"`); got != 3 {
		t.Fatalf("campaign-start logs = %d, want 3 actual starts", got)
	}
	// History is bounded; an evicted version can be retried later.
	for i := 0; i < maxRecentBackendHashes; i++ {
		hash.Store(fmt.Sprintf("%064x", i+100))
		check()
		wait()
	}
	if got := len(c.backendAttempts[h.server.ServerKey()].hashes); got != maxRecentBackendHashes {
		t.Fatalf("history length = %d", got)
	}
	before := downloads.Load()
	hash.Store(strings.Repeat("a", 64))
	check()
	wait()
	if downloads.Load() != before+1 {
		t.Fatal("evicted hash was not eligible for refresh")
	}

}

func TestBackendHashUnavailableAndMultiFeed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		multi  bool
	}{
		{"404", 404, "", false}, {"503", 503, "", false},
		{"missing", 200, `{}`, false}, {"invalid", 200, `{"staticGtfsHash":"bad"}`, false},
		{"malformed", 200, `{`, false}, {"multi", 200, `{"staticGtfsHash":"` + strings.Repeat("a", 64) + `"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			var requests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer ts.Close()
			h.server.ObaBaseURL = ts.URL
			if tc.multi {
				h.server.GtfsStaticFeeds = append(h.server.GtfsStaticFeeds, "second.zip")
			}
			h.store.SetConfiguredServers([]models.ObaServer{h.server})
			called := false
			h.service.BackendHashObserver = func(_ models.ObaServer, match *bool) {
				called = true
				if match != nil {
					t.Error("unavailable hash exported")
				}
			}
			h.service.CheckBackendBundles(context.Background(), context.Background(), []models.ObaServer{h.server})
			if !called {
				t.Fatal("unavailable observation missing")
			}
			if tc.multi && requests.Load() != 0 {
				t.Fatal("multi-feed backend queried")
			}
			if len(h.service.refreshCoordinator.backendAttempts) != 0 || len(h.service.refreshCoordinator.active) != 0 {
				t.Fatal("unavailable hash triggered campaign")
			}
		})
	}
}

func TestBackendHashConfigurationReconciliation(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	c := h.service.refreshCoordinator
	c.backendAttempts[h.server.ServerKey()] = backendHashAttempt{fingerprint: staticConfigFingerprint(h.server), hashes: []string{strings.Repeat("a", 64)}}
	changed := h.server
	changed.GtfsStaticFeeds = []string{"one.zip", "two.zip"}
	h.store.SetConfiguredServers([]models.ObaServer{changed})
	called := false
	h.service.BackendHashObserver = func(_ models.ObaServer, match *bool) {
		called = true
		if match != nil {
			t.Error("multi-feed gauge was not retired")
		}
	}
	h.service.ReconcileStaticRefreshCampaigns([]models.ObaServer{changed})
	if !called || len(c.backendAttempts) != 0 {
		t.Fatal("configuration change retained backend state")
	}
	c.backendAttempts[changed.ServerKey()] = backendHashAttempt{fingerprint: staticConfigFingerprint(changed), hashes: []string{strings.Repeat("b", 64)}}
	h.service.ReconcileStaticRefreshCampaigns(nil)
	if len(c.backendAttempts) != 0 {
		t.Fatal("removed server retained backend state")
	}
}

func TestBackendHashUnknownAndRetainedSnapshot(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	sum := sha256.Sum256(h.body.Load().([]byte))
	hash := hex.EncodeToString(sum[:])
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"staticGtfsHash":%q}`, hash) }))
	defer ts.Close()
	h.server.ObaBaseURL = ts.URL
	h.store.SetConfiguredServers([]models.ObaServer{h.server})
	var observed *bool
	h.service.SetBackendHashObserver(func(_ models.ObaServer, match *bool) { observed = match })
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	check := func() { h.service.CheckBackendBundles(context.Background(), stopped, []models.ObaServer{h.server}) }
	check()
	if observed != nil {
		t.Fatal("unknown local hash reported mismatch")
	}
	h.run(t, h.server, StaticRefreshStartup)
	check()
	if observed == nil || !*observed {
		t.Fatal("published hash did not match")
	}
	h.service.refreshCoordinator.forgetPublished(h.server)
	check()
	if observed == nil || !*observed {
		t.Fatal("failed refresh lost retained snapshot hash")
	}
}

func TestBackendHashSharedDeploymentAggregation(t *testing.T) {
	for _, kind := range []string{"match", "mismatch", "unknown", "different-feed", "multi-feed"} {
		t.Run(kind, func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			hash := strings.Repeat("a", 64)
			var requests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				fmt.Fprintf(w, `{"staticGtfsHash":%q}`, hash)
			}))
			defer ts.Close()
			h.server.ObaBaseURL = ts.URL
			sibling := h.server
			sibling.AgencyID = "B"
			if kind == "different-feed" {
				sibling.GtfsStaticFeeds = []string{"other.zip"}
			}
			if kind == "multi-feed" {
				sibling.GtfsStaticFeeds = []string{"one.zip", "two.zip"}
			}
			entries := []models.ObaServer{h.server, sibling}
			h.store.SetConfiguredServers(entries)
			c := h.service.refreshCoordinator
			c.recordPublished(context.Background(), h.server, []string{hash})
			siblingHash := hash
			if kind == "mismatch" {
				siblingHash = strings.Repeat("b", 64)
			}
			if kind != "unknown" {
				c.recordPublished(context.Background(), sibling, []string{siblingHash})
			}
			calls := 0
			var observed *bool
			h.service.SetBackendHashObserver(func(_ models.ObaServer, match *bool) { calls++; observed = match })
			stopped, cancel := context.WithCancel(context.Background())
			cancel()
			h.service.CheckBackendBundles(context.Background(), stopped, entries)
			if calls != 1 {
				t.Fatalf("gauge written %d times, want once", calls)
			}
			if kind == "match" || kind == "mismatch" {
				if observed == nil || *observed != (kind == "match") {
					t.Fatal("wrong aggregate gauge")
				}
			} else if observed != nil {
				t.Fatal("incomparable deployment exported gauge")
			}
			if (kind == "different-feed" || kind == "multi-feed") && requests.Load() != 0 {
				t.Fatal("ineligible deployment queried")
			}
		})
	}
}

func TestBackendHashConfigChangesDuringRequest(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprintf(w, `{"staticGtfsHash":%q}`, strings.Repeat("a", 64))
	}))
	defer ts.Close()
	h.server.ObaBaseURL = ts.URL
	h.store.SetConfiguredServers([]models.ObaServer{h.server})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.service.CheckBackendBundles(context.Background(), context.Background(), []models.ObaServer{h.server})
	}()
	<-started
	changed := h.server
	changed.GtfsStaticFeeds = []string{"replacement.zip"}
	h.store.SetConfiguredServers([]models.ObaServer{changed})
	h.service.ReconcileStaticRefreshCampaigns([]models.ObaServer{changed})
	c := h.service.refreshCoordinator
	canceled := false
	c.mu.Lock()
	c.active[changed.ServerKey()] = activeStaticCampaign{fingerprint: staticConfigFingerprint(changed), cancel: func() { canceled = true }}
	c.mu.Unlock()
	close(release)
	<-done
	if c.start(context.Background(), h.service, h.server, StaticRefreshBackendChanged, 1, strings.Repeat("b", 64)) {
		t.Fatal("stale campaign registered")
	}
	if canceled || len(c.backendAttempts) != 0 {
		t.Fatal("stale check changed current campaign state")
	}
}

func TestBackendHashObservationDoesNotWaitForPublication(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.service.SetBackendHashObserver(func(models.ObaServer, *bool) {})
	h.store.refreshMu.Lock()
	defer h.store.refreshMu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); h.service.observeBackendHash(h.server, nil) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observation waited for publication lock")
	}
}

// TestBackendMetadataLogging distinguishes unexpected metadata from ordinary unsupported backends.
func TestBackendMetadataLogging(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, reason string
	}{
		{"missing_endpoint", 404, "", ""}, {"unavailable", 503, "", ""}, {"no_hash", 200, `{}`, ""},
		{"malformed", 200, `{not-json`, "malformed_json"},
		{"invalid_hash", 200, `{"staticGtfsHash":"private-response"}`, "invalid_hash"},
		{"unauthorized", 401, "", `"status":401`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			var logs bytes.Buffer
			h.service.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer ts.Close()
			h.server.ObaBaseURL = ts.URL
			h.server.ObaApiKey = "private-key"
			if hash := h.service.fetchBackendHash(context.Background(), h.server); hash != "" {
				t.Fatal("invalid response returned a hash")
			}
			got := logs.String()
			if tc.reason == "" && got != "" {
				t.Fatalf("expected response logged: %s", got)
			}
			if tc.reason != "" && (!strings.Contains(got, tc.reason) || !strings.Contains(got, ts.URL)) {
				t.Fatalf("missing diagnostic: %s", got)
			}
			if strings.Contains(got, "private-key") || strings.Contains(got, "private-response") {
				t.Fatal("metadata diagnostics exposed sensitive values")
			}
		})
	}
}
