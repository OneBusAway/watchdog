package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// expiredTestFeed is a valid feed whose only service ended in January 2025.
func expiredTestFeed(t *testing.T) []byte {
	t.Helper()
	return staticTestFeed(t, "20250131")
}

// currentTestFeed is a valid feed whose service runs for another year.
func currentTestFeed(t *testing.T) []byte {
	t.Helper()
	return staticTestFeed(t, time.Now().UTC().AddDate(1, 0, 0).Format("20060102"))
}

// staticTestFeed is a valid one-agency feed whose service ends on endDate (YYYYMMDD).
func staticTestFeed(t *testing.T, endDate string) []byte {
	t.Helper()
	files := map[string]string{
		"agency.txt":     "agency_id,agency_name,agency_url,agency_timezone\nX,Expired Transit,https://example.com,UTC\n",
		"routes.txt":     "route_id,agency_id,route_short_name,route_type\nR,X,R,3\n",
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,1,1,20250101," + endDate + "\n",
		"trips.txt":      "route_id,service_id,trip_id\nR,WK,T\n",
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,1\nS2,Two,2,2\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,2\n",
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func feedHash(feed []byte) string {
	sum := sha256.Sum256(feed)
	return hex.EncodeToString(sum[:])
}

// expiryHarness is one agency whose feed URL and backend metadata hash the
// test controls, collected through the real tick loop.
type expiryHarness struct {
	app    *Application
	server models.ObaServer
	feed   atomic.Value // []byte; an empty slice serves 404
	hash   atomic.Value // string; "" serves no hash
	ctx    context.Context
}

func newExpiryHarness(t *testing.T, startupFeed []byte) *expiryHarness {
	t.Helper()
	h := &expiryHarness{}
	h.feed.Store(startupFeed)
	h.hash.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.zip":
			if feed := h.feed.Load().([]byte); len(feed) > 0 {
				w.Write(feed)
				return
			}
			http.NotFound(w, r)
		case "/api/v2/metadata.json":
			fmt.Fprintf(w, `{"staticGtfsHash":%q}`, h.hash.Load().(string))
		case "/api/where/current-time.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"currentTime":1234567890000,"data":{"entry":{"readableTime":"now"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	h.app = newEmptyApplication(t)
	h.server = models.ObaServer{ServerName: "expiry-test", AgencyID: "X", AgencyName: "Expired Transit", ObaBaseURL: ts.URL, ObaApiKey: "k", GtfsStaticFeeds: []string{ts.URL + "/feed.zip"}}
	h.app.ConfigService.Config.UpdateConfig([]models.ObaServer{h.server})
	h.app.GtfsService.StaticStore.SetConfiguredServers([]models.ObaServer{h.server})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.ctx = ctx
	// Start the way main does, so the bundle's hash is recorded.
	h.app.GtfsService.StartStaticRefreshCampaigns(ctx, []models.ObaServer{h.server}, gtfs.StaticRefreshStartup, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if check, ok := h.app.GtfsService.LastStaticFeedCheck(h.server); ok && check.Success {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture bundle was not published")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { metrics.DeleteSeriesForServer(utils.SanitizeServerURL(h.server.ObaBaseURL)) })
	return h
}

// collectUntil runs ticks until done reports true, or fails after a few seconds.
func (h *expiryHarness) collectUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.app.collectOnce(h.ctx)
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; entry = %+v", what, h.app.BundleExpiry.entry(h.server.ServerKey()))
}

// ignoreEarlierFeedChecks makes only feed checks that start after the expiry
// count, as in production when the startup download was long ago.
func ignoreEarlierFeedChecks(t *testing.T) {
	saved := feedCheckClockSkew
	feedCheckClockSkew = 0
	t.Cleanup(func() { feedCheckClockSkew = saved })
}

func TestExpiredBundleWithholdsStaticDerivedMetrics(t *testing.T) {
	h := newExpiryHarness(t, expiredTestFeed(t))
	server := h.server
	labels := map[string]string{"agency_id": server.AgencyID, "server_url": utils.SanitizeServerURL(server.ObaBaseURL)}
	// A value left over from before the bundle expired.
	metrics.RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)).Set(5)

	h.app.collectOnce(h.ctx)

	if _, found := gaugeValueFor(metrics.RealtimeVehiclePositions, labels); found {
		t.Fatal("an expired bundle left a stale vehicle count on /metrics")
	}
	if v, found := gaugeValueFor(metrics.GtfsScheduleAvailable, labels); !found || v != 0 {
		t.Fatalf("schedule available = %v (found %v), want 0 for an expired bundle", v, found)
	}
	if v, found := gaugeValueFor(metrics.GtfsStaticBundleUsable, labels); !found || v != 0 {
		t.Fatalf("bundle usable = %v (found %v), want 0", v, found)
	}
	if v, found := gaugeValueFor(metrics.BundleLatestExpirationGauge, labels); !found || v >= 0 {
		t.Fatalf("days until latest expiration = %v (found %v), want a negative value", v, found)
	}
	// The startup download checked the feed URL a moment ago and found this
	// same expired bundle, so the expiry is reported on the first tick.
	entry := h.app.BundleExpiry.entry(server.ServerKey())
	if entry.kind != withholdExpired || !entry.reported || entry.cause != withholdCauseNoNewerBundle {
		t.Fatalf("entry = %+v, want an expiry reported as no_newer_bundle", entry)
	}
}

// TestExpiredBundleCauseComesFromAFreshFeedCheck covers each answer the feed
// URL can give once the bundle has expired. The startup check predates the
// expiry, so the event waits for the fallback that the expiry starts at once,
// even while the backend hash matches.
func TestExpiredBundleCauseComesFromAFreshFeedCheck(t *testing.T) {
	ignoreEarlierFeedChecks(t)
	expired := expiredTestFeed(t)
	for _, tc := range []struct {
		name        string
		feedAfter   []byte // what the feed URL serves after startup; empty is 404
		backendHash string // "" means the backend's hash matches Watchdog's
		wantCause   string
		wantReason  string
	}{
		{"agency has not published", expired, "", withholdCauseNoNewerBundle, ""},
		{"feed gone, backend on the same bundle", []byte{}, "", withholdCauseFeedUnreachable, "not_found"},
		{"feed gone, backend on another bundle", []byte{}, strings.Repeat("b", 64), withholdCauseRefreshFailing, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newExpiryHarness(t, expired)
			backendHash := tc.backendHash
			if backendHash == "" {
				backendHash = feedHash(expired)
			}
			h.hash.Store(backendHash)
			h.feed.Store(tc.feedAfter)
			key := h.server.ServerKey()
			h.collectUntil(t, "the expiry to be reported", func() bool { return h.app.BundleExpiry.entry(key).reported })
			entry := h.app.BundleExpiry.entry(key)
			if entry.cause != tc.wantCause || entry.reason != tc.wantReason {
				t.Fatalf("cause = %q reason = %q, want %q %q", entry.cause, entry.reason, tc.wantCause, tc.wantReason)
			}
		})
	}
}

// TestBackendStaleAfterExpiry is the case from the review of #334: the agency
// publishes a newer bundle, the backend never loads it and keeps reporting
// the hash of the old one, which matches Watchdog's. Once the old bundle
// expires, Watchdog checks the feed URL, picks up the newer bundle, and
// reports the backend as stale instead of blaming the agency. Metrics stay
// withheld, because Watchdog and the backend now hold different bundles.
func TestBackendStaleAfterExpiry(t *testing.T) {
	ignoreEarlierFeedChecks(t)
	expired := expiredTestFeed(t)
	current := currentTestFeed(t)
	h := newExpiryHarness(t, expired)
	h.hash.Store(feedHash(expired))
	h.feed.Store(current)
	key := h.server.ServerKey()
	labels := map[string]string{"agency_id": h.server.AgencyID, "server_url": utils.SanitizeServerURL(h.server.ObaBaseURL)}

	// The stale hash must hold across a stable run before it is reported, so
	// each poll stands in for one 30-second collection interval.
	var elapsed atomic.Int64
	h.app.GtfsService.SetStaticRefreshClock(func() time.Time {
		return time.Now().Add(time.Duration(elapsed.Load()))
	})
	h.collectUntil(t, "the backend to be reported stale", func() bool {
		elapsed.Add(int64(30 * time.Second))
		entry := h.app.BundleExpiry.entry(key)
		return entry.kind == withholdBackendBehind && entry.reported
	})
	if entry := h.app.BundleExpiry.entry(key); entry.cause != withholdCauseBackendStale {
		t.Fatalf("cause = %q, want backend_stale", entry.cause)
	}
	if v, found := gaugeValueFor(metrics.GtfsScheduleAvailable, labels); !found || v != 0 {
		t.Fatalf("schedule available = %v (found %v), want 0 while the backend is behind", v, found)
	}

	// The backend loads the newer bundle: metrics resume.
	h.hash.Store(feedHash(current))
	h.collectUntil(t, "metrics to resume", func() bool { return h.app.BundleExpiry.entry(key).kind == withholdNone })
	if v, found := gaugeValueFor(metrics.GtfsScheduleAvailable, labels); !found || v != 1 {
		t.Fatalf("schedule available = %v (found %v), want 1 once the backend caught up", v, found)
	}
}

// TestExpiryWithoutFeedCheckIsReportedAfterAWait checks that an expiry is not
// held back forever when no feed URL check finishes.
func TestExpiryWithoutFeedCheckIsReportedAfterAWait(t *testing.T) {
	ignoreEarlierFeedChecks(t)
	h := newExpiryHarness(t, expiredTestFeed(t))
	since := time.Now().UTC()
	entry := withheldEntry{kind: withholdExpired, since: since}
	if _, _, decided := h.app.withholdCause(h.server, entry, since.Add(expiryCauseWait-time.Minute)); decided {
		t.Fatal("cause decided before the wait, without a feed check")
	}
	cause, _, decided := h.app.withholdCause(h.server, entry, since.Add(expiryCauseWait))
	if !decided || cause != withholdCauseFeedNotChecked {
		t.Fatalf("cause = %q (decided %v), want feed_not_checked", cause, decided)
	}
}

func TestBundleExpiryReporterTracksKindChanges(t *testing.T) {
	r := NewBundleExpiryReporter()
	const key = "https://oba.example|a"
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if before, after := r.update(key, withholdExpired, t0); before.kind != withholdNone || after.since != t0 {
		t.Fatalf("first expiry: before %+v after %+v", before, after)
	}
	r.markReported(key, withholdCauseNoNewerBundle, "")
	// Still expired: the entry, and that it was reported, carry over.
	if _, after := r.update(key, withholdExpired, t0.Add(time.Hour)); !after.reported || after.since != t0 {
		t.Fatalf("still expired: %+v", after)
	}
	// A new reason to withhold starts a new, unreported entry.
	if _, after := r.update(key, withholdBackendBehind, t0.Add(2*time.Hour)); after.reported || after.kind != withholdBackendBehind {
		t.Fatalf("backend behind: %+v", after)
	}
	if before, _ := r.update(key, withholdNone, t0.Add(3*time.Hour)); before.kind != withholdBackendBehind {
		t.Fatalf("recovery saw %+v", before)
	}
	r.update(key, withholdExpired, t0)
	r.Prune(func(string) bool { return false })
	if r.entry(key).kind != withholdNone {
		t.Fatal("pruned state was kept")
	}
}
