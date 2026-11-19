package gtfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// lockedBuffer collects logs written by both the test and campaign goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) contains(s string) bool { return b.count(s) > 0 }

func (b *lockedBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

// backendHashHarness serves backend metadata and the feed from one server and
// drives collection ticks against a controllable clock.
type backendHashHarness struct {
	*unchangedFeedHarness
	hash      atomic.Value
	downloads atomic.Int32
	observed  *bool
	clock     time.Time
	logs      *lockedBuffer
	ctx       context.Context
}

func newBackendHashHarness(t *testing.T) *backendHashHarness {
	t.Helper()
	h := &backendHashHarness{unchangedFeedHarness: newUnchangedFeedHarness(t), logs: &lockedBuffer{}, clock: time.Unix(1_700_000_000, 0)}
	h.hash.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/metadata.json" {
			if r.URL.Query().Get("key") != "test-key" {
				t.Error("missing API key")
			}
			fmt.Fprintf(w, `{"staticGtfsHash":%q}`, h.hash.Load().(string))
			return
		}
		h.downloads.Add(1)
		w.Write(h.body.Load().([]byte))
	}))
	t.Cleanup(ts.Close)
	h.server.ObaBaseURL = ts.URL
	h.server.ObaApiKey = "test-key"
	h.server.GtfsStaticFeeds = []string{ts.URL + "/feed.zip"}
	h.store.SetConfiguredServers([]models.ObaServer{h.server})
	h.service.Logger = slog.New(slog.NewJSONHandler(h.logs, nil))
	h.service.refreshCoordinator.now = func() time.Time { return h.clock }
	h.run(t, h.server, StaticRefreshStartup)
	h.service.BackendHashObserver = func(_ models.ObaServer, match *bool) { h.observed = match }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.ctx = ctx
	return h
}

// bundleHash returns the SHA-256 of a feed body, as a backend would report it.
func bundleHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// tick advances the clock by interval, runs one backend check, and waits for any campaign it started.
func (h *backendHashHarness) tick(t *testing.T, interval time.Duration) {
	t.Helper()
	h.clock = h.clock.Add(interval)
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
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

// ticks runs n ticks at the given interval.
func (h *backendHashHarness) ticks(t *testing.T, n int, interval time.Duration) {
	t.Helper()
	for i := 0; i < n; i++ {
		h.tick(t, interval)
	}
}

func (h *backendHashHarness) wantDownloads(t *testing.T, want int32, msg string) {
	t.Helper()
	if got := h.downloads.Load(); got != want {
		t.Fatalf("%s: downloads = %d, want %d", msg, got, want)
	}
}

func (h *backendHashHarness) wantMatch(t *testing.T, want bool, msg string) {
	t.Helper()
	if h.observed == nil || *h.observed != want {
		t.Fatalf("%s: observed match = %v, want %v", msg, h.observed, want)
	}
}

const (
	campaignStartLog = `"trigger":"backend_changed"`
	mismatchWarnLog  = `"msg":"Backend static GTFS bundle still differs from the configured feed after refresh"`
)

// TestBackendHashWaitsForStableRun covers a real bundle change: no campaign
// starts until the new hash has held for both the duration and the tick count.
func TestBackendHashWaitsForStableRun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{{"default_interval", 30 * time.Second}, {"short_interval", 1500 * time.Millisecond}, {"long_interval", 2 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBackendHashHarness(t)
			h.service.SetBackendCheckInterval(tc.interval)
			h.hash.Store(bundleHash(h.body.Load().([]byte)))
			h.tick(t, tc.interval)
			h.wantMatch(t, true, "matching backend")
			h.body.Store(withExtraZipEntry(t, h.body.Load().([]byte)))
			h.hash.Store(bundleHash(h.body.Load().([]byte)))
			// A run starts on the first observation, so the campaign may start
			// once the stable duration has elapsed since then.
			n := int(backendHashStableFor / tc.interval)
			if n < backendHashStableTicks-1 {
				n = backendHashStableTicks - 1
			}
			h.ticks(t, n, tc.interval)
			h.wantDownloads(t, 1, "before the run was stable")
			h.tick(t, tc.interval)
			h.wantDownloads(t, 2, "stable new hash")
			h.tick(t, tc.interval)
			h.wantMatch(t, true, "after refresh")
			if got := h.logs.count(campaignStartLog); got != 1 {
				t.Fatalf("campaign-start logs = %d, want 1", got)
			}
			if h.logs.count(mismatchWarnLog) != 0 {
				t.Fatal("matching refresh logged a mismatch warning")
			}
		})
	}
}

// TestBackendHashAlternatingNodes covers load-balanced nodes answering on alternate ticks.
func TestBackendHashAlternatingNodes(t *testing.T) {
	for _, withMatch := range []bool{true, false} {
		t.Run(fmt.Sprintf("one_matches_%t", withMatch), func(t *testing.T) {
			h := newBackendHashHarness(t)
			first := strings.Repeat("a", 64)
			if withMatch {
				first = bundleHash(h.body.Load().([]byte))
			}
			second := strings.Repeat("b", 64)
			for i := 0; i < 40; i++ {
				h.hash.Store([]string{first, second}[i%2])
				h.tick(t, 30*time.Second)
			}
			h.wantDownloads(t, 1, "alternating hashes")
		})
	}
}

// TestBackendHashPersistentMismatch covers a backend that settles on a hash
// the feed URL does not serve: one campaign, one warning, then an hourly retry.
func TestBackendHashPersistentMismatch(t *testing.T) {
	h := newBackendHashHarness(t)
	// A startup or fallback campaign in flight must not count as this run's campaign.
	c := h.service.refreshCoordinator
	c.active[h.server.ServerKey()] = activeStaticCampaign{fingerprint: staticConfigFingerprint(h.server), cancel: func() {}}
	h.hash.Store(strings.Repeat("a", 64))
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
	h.clock = h.clock.Add(30 * time.Second)
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
	h.clock = h.clock.Add(30 * time.Second)
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
	h.clock = h.clock.Add(30 * time.Second)
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
	h.wantDownloads(t, 1, "active campaign")
	delete(c.active, h.server.ServerKey())
	h.tick(t, 30*time.Second)
	h.wantDownloads(t, 2, "stable mismatch")
	h.ticks(t, 10, 30*time.Second)
	h.wantMatch(t, false, "persistent mismatch")
	h.wantDownloads(t, 2, "same run before the retry interval")
	if got := h.logs.count(mismatchWarnLog); got != 1 {
		t.Fatalf("mismatch warnings = %d, want 1", got)
	}
	if !h.logs.contains(`"backend_hash":"`+strings.Repeat("a", 64)) || h.logs.contains("test-key") {
		t.Fatal("warning is missing the backend hash or exposed the API key")
	}
	h.ticks(t, int(backendHashRetryEvery/(30*time.Second))-10, 30*time.Second)
	h.wantDownloads(t, 3, "hourly retry")
	h.tick(t, 30*time.Second)
	if got := h.logs.count(mismatchWarnLog); got != 2 {
		t.Fatalf("mismatch warnings = %d, want one per campaign", got)
	}
}

// TestBackendHashRetryLimitAcrossRuns covers a run that restarts while the
// same mismatch persists: the hourly retry limit and the single warning
// carry over to the new run.
func TestBackendHashRetryLimitAcrossRuns(t *testing.T) {
	t.Run("random_nodes", func(t *testing.T) {
		// One node is stuck on an old bundle and a load balancer picks
		// nodes at random, so the stale hash keeps forming new runs.
		h := newBackendHashHarness(t)
		pick := []string{strings.Repeat("a", 64), bundleHash(h.body.Load().([]byte))}
		rng := rand.New(rand.NewPCG(320, 0))
		for i := 0; i < int(2*backendHashRetryEvery/(30*time.Second)); i++ {
			h.hash.Store(pick[rng.IntN(2)])
			h.tick(t, 30*time.Second)
		}
		// The startup download plus at most one campaign per hour.
		if got := h.downloads.Load(); got > 3 {
			t.Fatalf("downloads over two hours = %d, want at most 3", got)
		}
	})
	t.Run("metadata_outage", func(t *testing.T) {
		h := newBackendHashHarness(t)
		h.hash.Store(strings.Repeat("a", 64))
		h.ticks(t, 5, 30*time.Second)
		h.wantDownloads(t, 2, "stable mismatch")
		h.hash.Store("")
		h.ticks(t, 10, 30*time.Second)
		h.hash.Store(strings.Repeat("a", 64))
		h.ticks(t, 10, 30*time.Second)
		h.wantDownloads(t, 2, "same mismatch after the outage")
		if got := h.logs.count(mismatchWarnLog); got != 1 {
			t.Fatalf("mismatch warnings = %d, want 1", got)
		}
	})
	t.Run("quick_rollback", func(t *testing.T) {
		// A rollback minutes after a deployment looks like a node flapping
		// behind a load balancer, so it waits for the hourly retry.
		h := newBackendHashHarness(t)
		bundleA := h.body.Load().([]byte)
		h.hash.Store(bundleHash(bundleA))
		h.tick(t, 30*time.Second)
		h.body.Store(withExtraZipEntry(t, bundleA))
		h.run(t, h.server, StaticRefreshFallback)
		h.ticks(t, 6, 30*time.Second)
		h.wantDownloads(t, 2, "no campaign while the backend lags")
		h.hash.Store(bundleHash(h.body.Load().([]byte)))
		h.ticks(t, 4, 30*time.Second)
		h.body.Store(bundleA)
		h.hash.Store(bundleHash(bundleA))
		h.ticks(t, 4, 30*time.Second)
		h.wantDownloads(t, 2, "rollback inside the retry interval")
		h.ticks(t, int(backendHashRetryEvery/(30*time.Second)), 30*time.Second)
		h.tick(t, 30*time.Second)
		h.wantMatch(t, true, "rolled back to A after the retry")
	})
}

// TestBackendHashRevertedFeed covers a feed URL that is corrected while the
// backend keeps reporting the same hash: the hourly retry recovers it.
func TestBackendHashRevertedFeed(t *testing.T) {
	h := newBackendHashHarness(t)
	bundleA := h.body.Load().([]byte)
	h.hash.Store(bundleHash(bundleA))
	h.tick(t, 30*time.Second)
	// The wrong bundle reaches the feed URL and a fallback refresh picks it up.
	h.body.Store(withExtraZipEntry(t, bundleA))
	h.run(t, h.server, StaticRefreshFallback)
	h.ticks(t, 4, 30*time.Second)
	// The backend serves A, the bundle Watchdog replaced, so Watchdog waits
	// for it instead of downloading again at once.
	h.wantDownloads(t, 2, "no immediate campaign while Watchdog is ahead")
	// The feed URL is put back; the backend never changed.
	h.body.Store(bundleA)
	h.ticks(t, 10, 30*time.Second)
	h.wantMatch(t, false, "before the retry")
	h.ticks(t, int(backendHashRetryEvery/(30*time.Second)), 30*time.Second)
	h.tick(t, 30*time.Second)
	h.wantMatch(t, true, "after the hourly retry")
	h.wantDownloads(t, 3, "one retry")
}

// TestBackendHashRollbackAfterDeployment is the sequence from #317: a refresh
// lands while a redeploy is in progress, and a later rollback must still refresh.
func TestBackendHashRollbackAfterDeployment(t *testing.T) {
	h := newBackendHashHarness(t)
	bundleA := h.body.Load().([]byte)
	hashA := bundleHash(bundleA)
	h.hash.Store(hashA)
	h.tick(t, 30*time.Second)
	// B is published to the feed URL; the backend still runs A during its
	// redeploy, and a fallback refresh lands in that window.
	bundleB := withExtraZipEntry(t, bundleA)
	h.body.Store(bundleB)
	h.run(t, h.server, StaticRefreshFallback)
	h.ticks(t, 6, 30*time.Second)
	// The backend still reports A, the bundle B replaced: Watchdog is ahead,
	// so it waits for the backend instead of downloading B again.
	h.wantDownloads(t, 2, "no campaign while the backend lags")
	if got := h.service.BackendBundleRelation(h.server); got != BackendBundleBehind {
		t.Fatalf("relation while the backend lags = %v, want behind", got)
	}
	// The redeploy finishes on B, which runs for a while.
	h.hash.Store(bundleHash(bundleB))
	h.ticks(t, int(backendHashSettledFor/(30*time.Second))+1, 30*time.Second)
	h.wantMatch(t, true, "deployment B")
	// B is broken and A is rolled back.
	h.body.Store(bundleA)
	h.hash.Store(hashA)
	h.ticks(t, 4, 30*time.Second)
	// The backend had caught up with B, so A is now a rollback to follow.
	h.wantDownloads(t, 3, "rollback")
	h.tick(t, 30*time.Second)
	h.wantMatch(t, true, "rolled back to A")
}

// TestBackendHashFeedAheadOfBackend covers a refresh that picks up a new
// bundle before the backend's redeploy finishes. The backend had matched for a
// long time, but its hash must hold against the new local bundle before a
// campaign starts, so a redeploy that finishes in time causes no download.
func TestBackendHashFeedAheadOfBackend(t *testing.T) {
	h := newBackendHashHarness(t)
	bundleA := h.body.Load().([]byte)
	h.hash.Store(bundleHash(bundleA))
	h.ticks(t, 20, 30*time.Second)
	bundleB := withExtraZipEntry(t, bundleA)
	h.body.Store(bundleB)
	h.run(t, h.server, StaticRefreshFallback)
	h.ticks(t, 2, 30*time.Second)
	h.wantMatch(t, false, "backend still on A")
	h.hash.Store(bundleHash(bundleB))
	h.ticks(t, 4, 30*time.Second)
	h.wantMatch(t, true, "redeploy finished")
	h.wantDownloads(t, 2, "only the startup and fallback downloads")
}

// TestBackendHashGapsAndMissingHashes checks that a tick without a hash
// neither ends nor extends a run, and that a long gap restarts it.
func TestBackendHashGapsAndMissingHashes(t *testing.T) {
	h := newBackendHashHarness(t)
	h.body.Store(withExtraZipEntry(t, h.body.Load().([]byte)))
	newHash := bundleHash(h.body.Load().([]byte))
	h.hash.Store(newHash)
	h.tick(t, 30*time.Second)
	h.hash.Store("")
	h.tick(t, 30*time.Second)
	h.hash.Store(newHash)
	h.tick(t, 30*time.Second)
	h.wantDownloads(t, 1, "two observations")
	h.tick(t, 30*time.Second)
	h.wantDownloads(t, 2, "a missing hash broke the run")

	// After a gap such as a collection backoff, stability must be shown again.
	h = newBackendHashHarness(t)
	h.hash.Store(strings.Repeat("a", 64))
	h.tick(t, 30*time.Second)
	h.clock = h.clock.Add(5 * time.Minute)
	h.ticks(t, 3, 30*time.Second)
	h.wantDownloads(t, 1, "run spanning a gap")
	h.tick(t, 30*time.Second)
	h.wantDownloads(t, 2, "restarted run")
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
			if len(h.service.refreshCoordinator.backendRuns) != 0 || len(h.service.refreshCoordinator.active) != 0 {
				t.Fatal("unavailable hash triggered campaign")
			}
		})
	}
}

func TestBackendHashConfigurationReconciliation(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	c := h.service.refreshCoordinator
	c.backendRuns[h.server.ServerKey()] = backendHashRun{fingerprint: staticConfigFingerprint(h.server), hash: strings.Repeat("a", 64)}
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
	if !called || len(c.backendRuns) != 0 {
		t.Fatal("configuration change retained backend state")
	}
	c.backendRuns[changed.ServerKey()] = backendHashRun{fingerprint: staticConfigFingerprint(changed), hash: strings.Repeat("b", 64)}
	h.service.ReconcileStaticRefreshCampaigns(nil)
	if len(c.backendRuns) != 0 {
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
			c.recordPublished(context.Background(), h.server, hash)
			siblingHash := hash
			if kind == "mismatch" {
				siblingHash = strings.Repeat("b", 64)
			}
			if kind != "unknown" {
				c.recordPublished(context.Background(), sibling, siblingHash)
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
	if c.start(context.Background(), h.service, h.server, StaticRefreshBackendChanged, 1) {
		t.Fatal("stale campaign registered")
	}
	if canceled || len(c.backendRuns) != 0 {
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
		want         BackendHashStatus
	}{
		{"missing_endpoint", 404, "", "", BackendHashUnsupported},
		{"unavailable", 503, "", "", BackendHashTransient},
		{"no_hash", 200, `{}`, "", BackendHashUnsupported},
		{"malformed", 200, `{not-json`, "malformed_json", BackendHashUnsupported},
		{"invalid_hash", 200, `{"staticGtfsHash":"private-response"}`, "invalid_hash", BackendHashUnsupported},
		{"unauthorized", 401, "", `"status":401`, BackendHashUnsupported},
		{"server_error", 500, "", `"status":500`, BackendHashTransient},
		{"throttled", 429, "", `"status":429`, BackendHashTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			var logs bytes.Buffer
			h.service.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer ts.Close()
			h.server.ObaBaseURL = ts.URL
			h.server.ObaApiKey = "private-key"
			hash, status := h.service.fetchBackendHash(context.Background(), h.server)
			if hash != "" {
				t.Fatal("invalid response returned a hash")
			}
			if status != tc.want {
				t.Fatalf("status = %s, want %s", status, tc.want)
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

// TestBackendMetadataRequestDiagnostics distinguishes an optional timeout from caller cancellation.
func TestBackendMetadataRequestDiagnostics(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller_canceled_%t", cancelCaller), func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			var logs bytes.Buffer
			h.service.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer ts.Close()
			h.server.ObaBaseURL = ts.URL
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelCaller {
				cancel()
			}
			_, status := h.service.fetchBackendHash(ctx, h.server)
			logged := strings.Contains(logs.String(), "request_failed")
			if logged == cancelCaller {
				t.Fatalf("request failure logged=%v for caller canceled=%v", logged, cancelCaller)
			}
			// A canceled caller says nothing about the backend; a timeout does.
			want := BackendHashTransient
			if cancelCaller {
				want = BackendHashNotChecked
			}
			if status != want {
				t.Fatalf("status = %s, want %s", status, want)
			}
		})
	}
}

// TestMatchingBackendHashConfirmsSnapshotCurrent covers the replacement for
// the daily download: a backend that reports the hash of the bundle Watchdog
// holds confirms it current without a download.
func TestMatchingBackendHashConfirmsSnapshotCurrent(t *testing.T) {
	h := newBackendHashHarness(t)
	stale := time.Now().UTC().Add(-48 * time.Hour)
	h.store.SetFetchTime(h.server.ServerKey(), stale)
	downloads := h.downloads.Load()

	h.hash.Store(bundleHash(h.body.Load().([]byte)))
	if status := h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server}); status != BackendHashOK {
		t.Fatalf("status = %s, want ok", status)
	}
	if fetched, _ := h.store.GetFetchTime(h.server.ServerKey()); !fetched.After(stale) {
		t.Fatal("a matching backend hash did not renew the snapshot's fetch time")
	}
	if h.downloads.Load() != downloads {
		t.Fatal("a matching backend hash downloaded the feed")
	}

	// A mismatch confirms nothing.
	h.store.SetFetchTime(h.server.ServerKey(), stale)
	h.hash.Store(strings.Repeat("cd", 32))
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{h.server})
	if fetched, _ := h.store.GetFetchTime(h.server.ServerKey()); !fetched.Equal(stale) {
		t.Fatal("a differing backend hash renewed the snapshot's fetch time")
	}
}
