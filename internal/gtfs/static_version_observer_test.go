package gtfs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// publicationRecorder collects the publications the static version observer
// receives.
type publicationRecorder struct {
	mu           sync.Mutex
	publications []StaticVersionPublication
}

func (r *publicationRecorder) observe(publication StaticVersionPublication) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publications = append(r.publications, publication)
}

func (r *publicationRecorder) all() []StaticVersionPublication {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StaticVersionPublication(nil), r.publications...)
}

func recordPublications(h *unchangedFeedHarness) *publicationRecorder {
	recorder := &publicationRecorder{}
	h.service.SetStaticVersionObserver(recorder.observe)
	return recorder
}

func TestStaticVersion_ObserverOnPublish(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	recorder := recordPublications(h)
	before := time.Now().UTC()

	h.run(t, h.server, StaticRefreshStartup)

	publications := recorder.all()
	if len(publications) != 1 {
		t.Fatalf("observer called %d times, want 1", len(publications))
	}
	got := publications[0]
	if got.Server.ServerKey() != h.server.ServerKey() {
		t.Errorf("server key = %q, want %q", got.Server.ServerKey(), h.server.ServerKey())
	}
	if got.PublishedAt.Before(before) {
		t.Errorf("PublishedAt = %v, before the campaign started at %v", got.PublishedAt, before)
	}
	fetchedAt, _ := h.store.GetFetchTime(h.server.ServerKey())
	if !got.PublishedAt.Equal(fetchedAt) {
		t.Errorf("PublishedAt = %v, want the snapshot's fetch time %v", got.PublishedAt, fetchedAt)
	}
	feed := h.body.Load().([]byte)
	if len(got.Feeds) != 1 || got.Feeds[0].URL != h.server.GtfsStaticFeeds[0] || got.Feeds[0].ContentSHA256 != contentSHA256(feed) {
		t.Errorf("Feeds = %+v, want the configured feed with hash %s", got.Feeds, contentSHA256(feed))
	}
	if len(got.Agencies) != 1 || got.Agencies[0].AgencyID != h.server.AgencyID {
		t.Errorf("Agencies = %+v, want agency %q", got.Agencies, h.server.AgencyID)
	}
	// The publication carries the boxes the snapshot set.
	stored, ok := h.service.BoundingBoxStore.Get(h.server.ServerKey())
	if !ok {
		t.Fatal("no bounding box for the published snapshot")
	}
	if carried, ok := got.BoundingBoxes[h.server.ServerKey()]; !ok || carried != stored {
		t.Errorf("BoundingBoxes[%q] = %+v (%v), want the stored %+v", h.server.ServerKey(), carried, ok, stored)
	}
}

// Recording waits on the database, so the hook must run once the global
// parse slot is free for other servers.
func TestStaticVersion_ObserverRunsOutsideParseSlot(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	var calls, admitted atomic.Int32
	h.service.SetStaticVersionObserver(func(StaticVersionPublication) {
		calls.Add(1)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if h.service.withStaticParseSlot(ctx, func() {}) {
			admitted.Add(1)
		}
	})

	h.run(t, h.server, StaticRefreshStartup)

	if calls.Load() != 1 || admitted.Load() != 1 {
		t.Fatalf("observer calls = %d, parse slot free in %d of them; want 1 and 1", calls.Load(), admitted.Load())
	}
}

// Byte-identical feeds only confirm the current snapshot: nothing new was
// published, so the observer is not called.
func TestStaticVersion_NotCalledOnConfirmation(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	recorder := recordPublications(h)
	h.run(t, h.server, StaticRefreshStartup)
	if len(recorder.all()) != 1 {
		t.Fatalf("setup: observer called %d times, want 1", len(recorder.all()))
	}

	h.run(t, h.server, StaticRefreshFallback)

	if got := len(recorder.all()); got != 1 {
		t.Fatalf("observer called %d times after an unchanged refresh, want 1", got)
	}
}

func TestStaticVersion_ObserverOnChangedContent(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	recorder := recordPublications(h)
	h.run(t, h.server, StaticRefreshStartup)

	changed := withExtraZipEntry(t, smallStaticFeed(t))
	h.body.Store(changed)
	h.run(t, h.server, StaticRefreshFallback)

	publications := recorder.all()
	if len(publications) != 2 {
		t.Fatalf("observer called %d times, want 2", len(publications))
	}
	if got := publications[1].Feeds[0].ContentSHA256; got != contentSHA256(changed) {
		t.Errorf("second publication hash = %s, want %s", got, contentSHA256(changed))
	}
	if publications[0].Feeds[0].ContentSHA256 == publications[1].Feeds[0].ContentSHA256 {
		t.Error("changed content published the same hash")
	}
}

func TestStaticVersion_ObserverOnDownloadGTFSBundles(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	recorder := recordPublications(h)

	h.service.DownloadGTFSBundles(t.Context(), []models.ObaServer{h.server}, 1)

	publications := recorder.all()
	if len(publications) != 1 {
		t.Fatalf("observer called %d times, want 1", len(publications))
	}
	if got, want := publications[0].Feeds[0].ContentSHA256, contentSHA256(h.body.Load().([]byte)); got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}

// Canceling the campaign after the snapshot is installed must still report
// the publication. The coordinator does not treat that campaign as the
// published hash, so it cannot overwrite a newer one.
func TestStaticVersion_RecordedWhenCanceledAfterInstall(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.SetFeedMappingObserver(func(models.ObaServer, StaticFeedMappingObservation) {
		cancel()
	})
	recorder := recordPublications(h)

	h.service.runStaticRefreshCampaign(ctx, h.server, StaticRefreshStartup, 1, fastStaticRefreshPolicy)

	if _, ok := h.store.Get(h.server.ServerKey()); !ok {
		t.Fatal("snapshot was not installed")
	}
	if got := len(recorder.all()); got != 1 {
		t.Fatalf("observer called %d times, want 1", got)
	}
	hash := contentSHA256(h.body.Load().([]byte))
	if h.service.refreshCoordinator.unchanged(h.server, hash) {
		t.Fatal("canceled campaign updated the published hash")
	}
}

// A panicking observer must not break publication.
func TestStaticVersion_ObserverPanicIsContained(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.service.SetStaticVersionObserver(func(StaticVersionPublication) { panic("observer failed") })

	h.run(t, h.server, StaticRefreshStartup)

	if state, ok := h.store.GetRefreshState(h.server); !ok || !state.Success {
		t.Fatalf("refresh state = %+v, want success despite the panicking observer", state)
	}
}
