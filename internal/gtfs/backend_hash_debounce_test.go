package gtfs

import (
	"testing"
	"time"
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
		if !h.service.BackendBundleDebouncing(h.server) {
			t.Fatalf("tick %d: expected debouncing to be true", i)
		}
	}
	h.tick(t, 30*time.Second)
	h.wantRelation(t, BackendBundleBehind, "after the stale hash held")
	if h.service.BackendBundleDebouncing(h.server) {
		t.Fatal("expected debouncing to be false once behind is confirmed")
	}
}

func TestBackendBehindIgnoresSingleLaggingNode(t *testing.T) {
	h, hashA, hashB := newBackendBehindHarness(t)
	for i := 0; i < 8; i++ {
		if i%2 == 0 {
			// One lagging node behind a load balancer still serves bundle A.
			// Re-instate the superseded record so each lagging-node read is
			// classified as behind (and debounced to unknown), rather than
			// differs after an intervening match clears it.
			h.service.refreshCoordinator.mu.Lock()
			h.service.refreshCoordinator.superseded[h.server.ServerKey()] = publishedStaticContent{
				fingerprint: staticConfigFingerprint(h.server),
				hash:        hashA,
			}
			h.service.refreshCoordinator.mu.Unlock()
			h.hash.Store(hashA)
		} else {
			h.hash.Store(hashB)
		}
		h.tick(t, 30*time.Second)
		if i%2 == 0 {
			h.wantRelation(t, BackendBundleUnknown, "lagging-node read before debounce completes")
			if !h.service.BackendBundleDebouncing(h.server) {
				t.Fatalf("read %d: expected debouncing to be true", i)
			}
		} else {
			h.wantRelation(t, BackendBundleMatch, "updated-node read matches")
			if h.service.BackendBundleDebouncing(h.server) {
				t.Fatalf("read %d: expected debouncing to be false", i)
			}
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
