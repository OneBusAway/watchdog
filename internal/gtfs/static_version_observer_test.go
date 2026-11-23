package gtfs

import (
	"sync"
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
	// The hook fires after the bounding boxes are updated.
	if _, ok := h.service.BoundingBoxStore.Get(h.server.ServerKey()); !ok {
		t.Error("no bounding box for the published snapshot")
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

// A panicking observer must not break publication.
func TestStaticVersion_ObserverPanicIsContained(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	h.service.SetStaticVersionObserver(func(StaticVersionPublication) { panic("observer failed") })

	h.run(t, h.server, StaticRefreshStartup)

	if state, ok := h.store.GetRefreshState(h.server); !ok || !state.Success {
		t.Fatalf("refresh state = %+v, want success despite the panicking observer", state)
	}
}
