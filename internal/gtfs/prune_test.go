package gtfs

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func keepOnly(kept ...string) func(string) bool {
	set := make(map[string]bool, len(kept))
	for _, k := range kept {
		set[k] = true
	}
	return func(key string) bool { return set[key] }
}

// TestStaticStorePruneDropsUnknownKeys covers the leak: entries for servers
// that have left the configuration are never removed, so a long-running
// Watchdog holds parsed bundles for servers it no longer monitors.
func TestStaticStorePruneDropsUnknownKeys(t *testing.T) {
	store := NewStaticStore()
	store.Set("keep|agency-a", &models.StaticData{})
	store.SetFetchTime("keep|agency-a", time.Now().UTC())
	store.Set("drop|agency-b", &models.StaticData{})
	store.SetFetchTime("drop|agency-b", time.Now().UTC())

	removed := store.Prune(keepOnly("keep|agency-a"))

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed, got %v", removed)
	}
	if _, ok := store.Get("drop|agency-b"); ok {
		t.Fatal("expected the stale bundle to be gone")
	}
	if _, ok := store.GetFetchTime("drop|agency-b"); ok {
		t.Fatal("expected the stale fetch time to be gone too")
	}
	if _, ok := store.Get("keep|agency-a"); !ok {
		t.Fatal("expected the configured bundle to survive")
	}
	if _, ok := store.GetFetchTime("keep|agency-a"); !ok {
		t.Fatal("expected the configured fetch time to survive")
	}
}

func TestRealtimeStorePruneDropsUnknownKeys(t *testing.T) {
	store := NewRealtimeStore()
	store.Set("keep|", &models.RealtimeData{})
	store.Set("drop|", &models.RealtimeData{})

	removed := store.Prune(keepOnly("keep|"))

	if len(removed) != 1 || removed[0] != "drop|" {
		t.Fatalf("expected drop| to be reported as removed, got %v", removed)
	}
	if store.Get("drop|") != nil {
		t.Fatal("expected the stale feed to be gone")
	}
	if store.Get("keep|") == nil {
		t.Fatal("expected the configured feed to survive")
	}
}

// TestRouteAgencyIndexPruneServersDropsUnknownKeys guards composite-key index
// ownership during pruning.
func TestRouteAgencyIndexPruneServersDropsUnknownKeys(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Replace(models.ServerKey("https://keep.example.com", "agency-a"), map[string]string{"r1": "agency-a"}, nil, nil)
	idx.Replace(models.ServerKey("https://drop.example.com", "agency-b"), map[string]string{"r2": "agency-b"}, nil, nil)

	removed := idx.PruneServers(keepOnly(models.ServerKey("https://keep.example.com", "agency-a")))

	if len(removed) != 1 || removed[0] != models.ServerKey("https://drop.example.com", "agency-b") {
		t.Fatalf("expected the stale server key to be reported as removed, got %v", removed)
	}
	if _, ok := idx.Get(models.ServerKey("https://drop.example.com", "agency-b"), "r2"); ok {
		t.Fatal("expected the stale route mapping to be gone")
	}
	if _, ok := idx.Get(models.ServerKey("https://keep.example.com", "agency-a"), "r1"); !ok {
		t.Fatal("expected the configured route mapping to survive")
	}
}
