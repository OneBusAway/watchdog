package gtfs

import (
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

// TestLastStaticFeedCheckRecordsEachAttempt checks that the outcome of the
// last feed attempt is kept, whatever started it, so an expired bundle can be
// explained from what the feed URL actually returned.
func TestLastStaticFeedCheckRecordsEachAttempt(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	if _, ok := h.service.LastStaticFeedCheck(h.server); ok {
		t.Fatal("a feed check was reported before any attempt")
	}

	h.run(t, h.server, StaticRefreshStartup)
	check, ok := h.service.LastStaticFeedCheck(h.server)
	if !ok || !check.Success || check.Reason != "" {
		t.Fatalf("after a good download: check = %+v (found %v), want success", check, ok)
	}

	h.body.Store([]byte("not a zip"))
	h.run(t, h.server, StaticRefreshFallback)
	check, ok = h.service.LastStaticFeedCheck(h.server)
	if !ok || check.Success || check.Reason != StaticReasonInvalidZIP {
		t.Fatalf("after a bad download: check = %+v (found %v), want an invalid_zip failure", check, ok)
	}

	// An unchanged download is a successful check too.
	h.body.Store(smallStaticFeed(t))
	h.run(t, h.server, StaticRefreshFallback)
	if check, _ = h.service.LastStaticFeedCheck(h.server); !check.Success {
		t.Fatalf("after an unchanged download: check = %+v, want success", check)
	}
}

// TestStaticSyncLookupFallsBackToServerScope checks that an agency entry of a
// server-scoped configuration reads the state its server entry recorded.
func TestStaticSyncLookupFallsBackToServerScope(t *testing.T) {
	h := newUnchangedFeedHarness(t)
	serverScoped := h.server
	serverScoped.AgencyID, serverScoped.AgencyName = "", ""
	h.service.refreshCoordinator.recordFeedCheck(serverScoped, h.service.refreshCoordinator.now(), nil)
	h.service.refreshCoordinator.mu.Lock()
	h.service.refreshCoordinator.setRelationLocked(serverScoped.ServerKey(), staticConfigFingerprint(serverScoped), BackendBundleBehind)
	h.service.refreshCoordinator.mu.Unlock()

	agency := models.ObaServer{ServerName: h.server.ServerName, AgencyID: "B", ObaBaseURL: h.server.ObaBaseURL, GtfsStaticFeeds: h.server.GtfsStaticFeeds}
	if _, ok := h.service.LastStaticFeedCheck(agency); !ok {
		t.Fatal("agency entry did not see its server entry's feed check")
	}
	if got := h.service.BackendBundleRelation(agency); got != BackendBundleBehind {
		t.Fatalf("relation = %v, want behind", got)
	}
}
