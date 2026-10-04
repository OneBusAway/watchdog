package gtfs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestStaticArtifactCacheRoundTripAndCleanup(t *testing.T) {
	cache, err := newStaticArtifactCache(1024)
	if err != nil {
		t.Fatal(err)
	}
	dir := cache.dir
	artifact, err := cache.Put("https://example.com/feed.zip", []byte("zip data"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.hash == "" || artifact.size != 8 {
		t.Fatalf("unexpected artifact metadata: %+v", artifact)
	}
	data, err := cache.Read("https://example.com/feed.zip")
	if err != nil || string(data) != "zip data" {
		t.Fatalf("cache read = %q, %v", data, err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Read("https://example.com/feed.zip"); err == nil {
		t.Fatal("cache read succeeded after cleanup")
	}
	if dir == "" {
		t.Fatal("cache did not use a private temporary directory")
	}
}

func TestStaticArtifactCacheKeepsIdenticalFeedsIndependent(t *testing.T) {
	cache, err := newStaticArtifactCache(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if _, err := cache.Put("https://example.com/a.zip", []byte("same")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Put("https://example.com/b.zip", []byte("same")); err != nil {
		t.Fatal(err)
	}
	cache.Delete("https://example.com/a.zip")
	if data, err := cache.Read("https://example.com/b.zip"); err != nil || string(data) != "same" {
		t.Fatalf("second identical artifact = %q, %v", data, err)
	}
}

func TestStaticRefreshCampaignReusesSuccessfulFeed(t *testing.T) {
	bundle := readFixture(t, "gtfs.zip")
	var goodRequests atomic.Int32
	var flakyRequests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.zip":
			goodRequests.Add(1)
			_, _ = w.Write(bundle)
		case "/flaky.zip":
			if flakyRequests.Add(1) == 1 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	server := models.ObaServer{
		ServerName: "OBA", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL,
		GtfsStaticFeeds: []string{ts.URL + "/good.zip", ts.URL + "/flaky.zip"},
	}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{
		initialDelay: time.Millisecond,
		maxDelay:     time.Millisecond,
		budget:       time.Minute,
	})

	if got := goodRequests.Load(); got != 1 {
		t.Fatalf("successful feed requests = %d, want 1", got)
	}
	if got := flakyRequests.Load(); got != 2 {
		t.Fatalf("flaky feed requests = %d, want 2", got)
	}
	if _, ok := store.GetFetchTime(server.ServerKey()); !ok {
		t.Fatal("complete campaign did not publish the static snapshot")
	}
}

func TestStaticRefreshFailurePolicy(t *testing.T) {
	server := models.ObaServer{ObaBaseURL: "https://oba.example", AgencyID: "a"}
	coordinator := newStaticRefreshCoordinator()
	now := time.Now().UTC()

	auth := coordinator.recordFailure(server, "https://feeds.example/auth.zip", &StaticFeedError{Stage: StaticFailureHTTP, Reason: StaticReasonUnauthorized}, now)
	if !auth.conservative {
		t.Fatal("authentication failure did not enter conservative mode immediately")
	}
	invalidURL := coordinator.recordFailure(server, "::invalid", &StaticFeedError{Stage: StaticFailureRequest, Reason: StaticReasonInvalidURL}, now)
	if !invalidURL.conservative {
		t.Fatal("invalid URL did not enter conservative mode immediately")
	}

	missingURL := "https://feeds.example/missing.zip"
	coordinator.recordFailure(server, missingURL, &StaticFeedError{Stage: StaticFailureHTTP, Reason: StaticReasonNotFound}, now)
	coordinator.recordFailedCampaign(server, []string{missingURL}, StaticRefreshDaily)
	if !coordinator.isConservative(server, missingURL) {
		t.Fatal("404 did not enter conservative mode after one failed daily campaign")
	}

	invalid := &StaticFeedError{Stage: StaticFailureArchive, Reason: StaticReasonInvalidZIP, ContentHash: "same"}
	for attempt := 1; attempt <= 3; attempt++ {
		history := coordinator.recordFailure(server, "https://feeds.example/invalid.zip", invalid, now)
		if history.conservative != (attempt == 3) {
			t.Fatalf("attempt %d conservative = %t", attempt, history.conservative)
		}
	}
	changedInvalid := &StaticFeedError{Stage: StaticFailureArchive, Reason: StaticReasonInvalidZIP, ContentHash: "changed-but-still-invalid"}
	if history := coordinator.recordFailure(server, "https://feeds.example/invalid.zip", changedInvalid, now); !history.conservative {
		t.Fatal("changed invalid content incorrectly cleared conservative mode")
	}

	transientURL := "https://feeds.example/transient.zip"
	for run := 1; run <= 7; run++ {
		coordinator.recordFailure(server, transientURL, &StaticFeedError{Stage: StaticFailureHTTP, Reason: StaticReasonServerError}, now)
		coordinator.recordFailedCampaign(server, []string{transientURL}, StaticRefreshDaily)
		if got := coordinator.isConservative(server, transientURL); got != (run == 7) {
			t.Fatalf("daily failure %d conservative = %t", run, got)
		}
	}
}

func TestSuccessfulCompleteCampaignClearsConservativeMode(t *testing.T) {
	bundle := readFixture(t, "gtfs.zip")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) }))
	defer ts.Close()
	server := models.ObaServer{ServerName: "OBA", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/gtfs.zip"}}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	service.refreshCoordinator.recordFailure(server, server.GtfsStaticFeeds[0], &StaticFeedError{Stage: StaticFailureHTTP, Reason: StaticReasonUnauthorized}, time.Now())

	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshDaily, 1, staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Minute})

	if service.refreshCoordinator.isConservative(server, server.GtfsStaticFeeds[0]) {
		t.Fatal("complete successful recovery did not clear conservative mode")
	}
}

func TestStaticRefreshCampaignCountsIdenticalScheduleFailures(t *testing.T) {
	bundle := makeScheduleZip(t, basicScheduleFiles("Not/A-Timezone", "08:00:00", "09:00:00"))
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(bundle)
	}))
	defer ts.Close()

	server := models.ObaServer{ServerName: "OBA", AgencyID: "A", AgencyName: "Agency A", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/gtfs.zip"}}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Minute})

	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 identical validation attempts", got)
	}
	if !service.refreshCoordinator.isConservative(server, server.GtfsStaticFeeds[0]) {
		t.Fatal("third identical invalid schedule did not enter conservative mode")
	}
}

func TestStaticRefreshCampaignBoundsRetryAfterByBudget(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer ts.Close()

	server := models.ObaServer{ServerName: "OBA", AgencyID: "A", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/gtfs.zip"}}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	started := time.Now()
	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: 10 * time.Millisecond})

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("campaign exceeded budget while honoring Retry-After: %s", elapsed)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("120", now); got != 2*time.Minute {
		t.Fatalf("seconds Retry-After = %s", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now); got != time.Minute {
		t.Fatalf("date Retry-After = %s", got)
	}
}

// Every campaign downloads and fully parses its feeds, and a production merged
// feed costs hundreds of MB of heap to parse. Startup and the daily refresh
// start a campaign for every configured server at once; letting them overlap
// stacked those peaks past Watchdog's memory limit. Attempts must run one at a
// time, from first download through publish.
func TestStaticRefreshCampaignsDoNotOverlap(t *testing.T) {
	bundle := readFixture(t, "gtfs.zip")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write(bundle)
	}))
	defer ts.Close()

	var servers []models.ObaServer
	for _, name := range []string{"a", "b", "c", "d"} {
		servers = append(servers, models.ObaServer{
			ServerName: name, AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL + "/" + name,
			GtfsStaticFeeds: []string{ts.URL + "/" + name + ".zip"},
		})
	}
	store := NewStaticStore()
	store.SetConfiguredServers(servers)
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())

	var active, maxActive, finished atomic.Int32
	service.SetStaticRefreshObserver(func(_ models.ObaServer, observation StaticRefreshObservation) {
		switch {
		case observation.Retrying:
			now := active.Add(1)
			for {
				seen := maxActive.Load()
				if now <= seen || maxActive.CompareAndSwap(seen, now) {
					break
				}
			}
		case observation.Success, observation.GaveUp:
			active.Add(-1)
			finished.Add(1)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.StartStaticRefreshCampaigns(ctx, servers, StaticRefreshStartup, 1)

	deadline := time.Now().Add(10 * time.Second)
	for finished.Load() < int32(len(servers)) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d campaigns finished", finished.Load(), len(servers))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("%d campaign attempts ran concurrently, want 1", got)
	}
	for _, server := range servers {
		if _, ok := store.GetFetchTime(server.ServerKey()); !ok {
			t.Fatalf("campaign for %s did not publish", server.ServerName)
		}
	}
}

// A daily refresh first probes only the feeds in conservative mode; once the
// probe succeeds it goes straight on to fetch the rest within the same attempt.
func TestDailyConservativeProbeContinuesToRemainingFeeds(t *testing.T) {
	bundle := readFixture(t, "gtfs.zip")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bundle)
	}))
	defer ts.Close()

	server := models.ObaServer{
		ServerName: "OBA", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL,
		GtfsStaticFeeds: []string{ts.URL + "/probed.zip", ts.URL + "/rest.zip"},
	}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	service.refreshCoordinator.recordFailure(server, server.GtfsStaticFeeds[0], &StaticFeedError{Stage: StaticFailureRequest, Reason: StaticReasonForbidden}, time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service.runStaticRefreshCampaign(ctx, server, StaticRefreshDaily, 1, staticRefreshRetryPolicy{
		initialDelay: time.Millisecond,
		maxDelay:     time.Millisecond,
		budget:       time.Minute,
	})

	if ctx.Err() != nil {
		t.Fatal("campaign stalled after its conservative probe succeeded")
	}
	if _, ok := store.GetFetchTime(server.ServerKey()); !ok {
		t.Fatal("campaign did not publish after its conservative probe succeeded")
	}
}
