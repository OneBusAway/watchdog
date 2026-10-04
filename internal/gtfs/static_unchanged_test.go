package gtfs

import (
	"archive/zip"
	"bytes"
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

var fastStaticRefreshPolicy = staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Minute}

// feedServer serves whatever bytes body currently holds, or a 404 when it is
// empty, so a test can change a feed's content between campaigns.
func feedServer(t *testing.T, body *atomic.Value) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := body.Load().([]byte)
		if len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// withExtraZipEntry returns a copy of a feed zip that differs byte-for-byte
// but parses to the same static data.
func withExtraZipEntry(t *testing.T, feed []byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(feed), int64(len(feed)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, file := range reader.File {
		if err := writer.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	extra, err := writer.Create("watchdog_test_marker.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = extra.Write([]byte("changed"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type unchangedFeedHarness struct {
	server  models.ObaServer
	store   *StaticStore
	service *GtfsService
	body    *atomic.Value
	ts      *httptest.Server
}

func newUnchangedFeedHarness(t *testing.T) *unchangedFeedHarness {
	t.Helper()
	body := &atomic.Value{}
	body.Store(smallStaticFeed(t))
	ts := feedServer(t, body)
	server := models.ObaServer{
		ServerName: "OBA", AgencyID: "A", AgencyName: "Agency A", ObaBaseURL: ts.URL,
		GtfsStaticFeeds: []string{ts.URL + "/feed.zip"},
	}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	return &unchangedFeedHarness{server: server, store: store, service: service, body: body, ts: ts}
}

func (h *unchangedFeedHarness) run(t *testing.T, server models.ObaServer, trigger StaticRefreshTrigger) {
	t.Helper()
	h.service.runStaticRefreshCampaign(context.Background(), server, trigger, 1, fastStaticRefreshPolicy)
}

func (h *unchangedFeedHarness) published(t *testing.T) *models.StaticData {
	t.Helper()
	data, ok := h.store.Get(h.server.ServerKey())
	if !ok {
		t.Fatal("no static snapshot is published")
	}
	return data
}

// A daily refresh that downloads byte-identical feeds must not re-parse and
// re-publish them: that parse costs hundreds of MB on production feeds and
// replaces every static store for no change. It still counts as a successful
// refresh, so the bundle keeps reporting as current.
func TestDailyRefreshOfUnchangedFeedsSkipsPublish(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.run(t, h.server, StaticRefreshStartup)
	before := h.published(t)
	stale := time.Now().UTC().Add(-time.Hour)
	h.store.SetFetchTime(h.server.ServerKey(), stale)

	h.run(t, h.server, StaticRefreshDaily)

	if h.published(t) != before {
		t.Fatal("unchanged feeds were re-published")
	}
	fetchedAt, ok := h.store.GetFetchTime(h.server.ServerKey())
	if !ok || !fetchedAt.After(stale) {
		t.Fatalf("fetch time = %v, want it advanced past %v", fetchedAt, stale)
	}
	if state, ok := h.store.GetRefreshState(h.server); !ok || !state.Success {
		t.Fatalf("refresh state = %+v, want success", state)
	}
}

func TestRefreshPublishesWhenFeedContentChanges(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.run(t, h.server, StaticRefreshStartup)
	before := h.published(t)

	h.body.Store(withExtraZipEntry(t, smallStaticFeed(t)))
	h.run(t, h.server, StaticRefreshDaily)

	if h.published(t) == before {
		t.Fatal("changed feed content was not published")
	}
}

func TestRefreshPublishesWhenServerConfigurationChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(server models.ObaServer, feedURL string) models.ObaServer
	}{
		{"agency name", func(server models.ObaServer, _ string) models.ObaServer {
			server.AgencyName = "Renamed Agency"
			return server
		}},
		{"feed list", func(server models.ObaServer, feedURL string) models.ObaServer {
			server.GtfsStaticFeeds = []string{feedURL + "/feed.zip", feedURL + "/second.zip"}
			return server
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newUnchangedFeedHarness(t)
			h.run(t, h.server, StaticRefreshStartup)
			before := h.published(t)

			changed := tc.mutate(h.server, h.ts.URL)
			h.store.SetConfiguredServers([]models.ObaServer{changed})
			h.run(t, changed, StaticRefreshDaily)

			if h.published(t) == before {
				t.Fatal("a configuration change with identical feed bytes was not published")
			}
		})
	}
}

// A campaign that gives up marks the schedule unavailable, so the next
// successful download must publish even when its bytes match the last
// published content.
func TestRefreshPublishesAfterAFailedCampaignEvenWhenUnchanged(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.run(t, h.server, StaticRefreshStartup)
	before := h.published(t)

	feed := h.body.Load().([]byte)
	h.body.Store([]byte{})
	h.service.runStaticRefreshCampaign(context.Background(), h.server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond})
	if state, _ := h.store.GetRefreshState(h.server); !state.GaveUp {
		t.Fatalf("setup: failing campaign did not give up: %+v", state)
	}

	h.body.Store(feed)
	h.run(t, h.server, StaticRefreshStartup)

	if h.published(t) == before {
		t.Fatal("identical feeds were not re-published after a failed campaign")
	}
}

// Removing a server through --config-url must drop what Watchdog remembers
// about its published content, so re-adding it publishes from scratch.
func TestReconcileForgetsPublishedContentOfRemovedServers(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.run(t, h.server, StaticRefreshStartup)
	before := h.published(t)

	h.service.ReconcileStaticRefreshCampaigns(nil)
	h.run(t, h.server, StaticRefreshDaily)

	if h.published(t) == before {
		t.Fatal("a re-added server's unchanged feeds were not re-published")
	}
}

// Server-scoped entries store one snapshot per agency declared in agency.txt,
// none under the agency-less server key, so confirming an unchanged refresh
// must renew every agency's fetch time.
func TestDailyRefreshOfUnchangedFeedsRenewsEveryServerScopedAgency(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	server := h.server
	server.AgencyID, server.AgencyName = "", ""
	h.server = server
	h.store.SetConfiguredServers([]models.ObaServer{server})
	h.run(t, server, StaticRefreshStartup)

	stale := time.Now().UTC().Add(-time.Hour)
	before := map[string]*models.StaticData{}
	h.store.Range(func(key string, data *models.StaticData) bool {
		before[key] = data
		return true
	})
	if len(before) == 0 {
		t.Fatal("setup: server-scoped startup published no agency snapshots")
	}
	for key := range before {
		h.store.SetFetchTime(key, stale)
	}

	h.run(t, server, StaticRefreshDaily)

	for key, data := range before {
		if current, _ := h.store.Get(key); current != data {
			t.Fatalf("%s: unchanged feeds were re-published", key)
		}
		if fetchedAt, _ := h.store.GetFetchTime(key); !fetchedAt.After(stale) {
			t.Fatalf("%s: fetch time = %v, want it advanced past %v", key, fetchedAt, stale)
		}
	}
}

// smallStaticFeed is a minimal valid feed for agency A. These tests compare
// feed hashes, and a full fixture parse takes seconds under -race.
func smallStaticFeed(t *testing.T) []byte {
	return makeScheduleZip(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
}
