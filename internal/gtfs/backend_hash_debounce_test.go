package gtfs

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// newBackendBehindHarness leaves Watchdog on bundle B after replacing bundle A,
// with the backend still reporting A. It returns the hashes of A and B.
func newBackendBehindHarness(t *testing.T) (*backendHashHarness, string, string) {
	t.Helper()
	h := newBackendHashHarness(t)
	bundleA := h.body.Load().([]byte)
	hashA := bundleHash(bundleA)
	h.hash.Store(hashA)
	h.tick(t, 30*time.Second)
	bundleB := withExtraZipEntry(t, bundleA)
	h.body.Store(bundleB)
	h.run(t, h.server, StaticRefreshFallback)
	return h, hashA, bundleHash(bundleB)
}

func (h *backendHashHarness) wantRelation(t *testing.T, want BackendBundleRelation, msg string) {
	t.Helper()
	if got := h.service.BackendBundleRelation(h.server); got != want {
		t.Fatalf("%s: relation = %v, want %v", msg, got, want)
	}
}

func TestBackendBehindRequiresStableRun(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	// The run needs three reads spanning 90 seconds, so four reads at 30s.
	for i := 0; i < 3; i++ {
		h.tick(t, 30*time.Second)
		h.wantRelation(t, BackendBundleUnknown, "before the stale hash has held")
	}
	h.tick(t, 30*time.Second)
	h.wantRelation(t, BackendBundleBehind, "after the stale hash held")
}

func TestBackendBehindDebounceFlag(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	for i := 0; i < 3; i++ {
		h.tick(t, 30*time.Second)
		if !h.service.BackendBundleDebouncing(h.server) {
			t.Fatalf("tick %d: expected debouncing to be true while stale hash run is pending", i)
		}
	}
	h.tick(t, 30*time.Second)
	if h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be false once behind is confirmed")
	}
}

func TestBackendBehindIgnoresSingleLaggingNode(t *testing.T) {
	h, hashA, hashB := newBackendBehindHarness(t)
	for i := 0; i < 8; i++ {
		if i%2 == 0 {
			h.hash.Store(hashA)
		} else {
			h.hash.Store(hashB)
		}
		h.tick(t, 30*time.Second)
		if got := h.service.BackendBundleRelation(h.server); got == BackendBundleBehind {
			t.Fatalf("read %d: nodes answering with different hashes reported the backend as behind", i)
		}
	}
}

func TestBackendBehindSurvivesMetadataGap(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	h.ticks(t, 4, 30*time.Second)
	h.wantRelation(t, BackendBundleBehind, "confirmed")
	// A gap longer than the run tolerates restarts the run. The backend still
	// reports the stale hash, so it must not look recovered.
	h.tick(t, 10*time.Minute)
	h.wantRelation(t, BackendBundleBehind, "after a metadata gap")
}

func TestBackendBehindDebounceClearedWhenUnsupported(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	h.tick(t, 30*time.Second)
	if !h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be true while stale hash run is pending")
	}

	h.hash.Store("")
	h.tick(t, 30*time.Second)
	if h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be cleared when backend reports unsupported hash")
	}
	h.wantRelation(t, BackendBundleUnknown, "after backend stops reporting hash")
}

func TestBackendBehindDebounceClearedWhenIneligible(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	h.tick(t, 30*time.Second)
	if !h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be true while stale hash run is pending")
	}

	ineligibleServer := h.server
	ineligibleServer.GtfsStaticFeeds = nil
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{ineligibleServer})
	if h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be cleared when deployment is ineligible")
	}
	h.wantRelation(t, BackendBundleUnknown, "after deployment becomes ineligible")
}

func TestBackendBehindConfirmedDroppedWhenUnsupported(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	h.ticks(t, 4, 30*time.Second)
	h.wantRelation(t, BackendBundleBehind, "confirmed")

	h.hash.Store("")
	h.tick(t, 30*time.Second)
	h.wantRelation(t, BackendBundleUnknown, "after backend stops reporting hash")
}

func TestBackendBehindConfirmedDroppedWhenIneligible(t *testing.T) {
	h, _, _ := newBackendBehindHarness(t)
	h.ticks(t, 4, 30*time.Second)
	h.wantRelation(t, BackendBundleBehind, "confirmed")

	ineligibleServer := h.server
	ineligibleServer.GtfsStaticFeeds = nil
	h.service.CheckBackendBundles(h.ctx, h.ctx, []models.ObaServer{ineligibleServer})
	h.wantRelation(t, BackendBundleUnknown, "after deployment becomes ineligible")
}
