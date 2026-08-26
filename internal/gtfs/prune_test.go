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

// TestRouteAgencyIndexPruneServersDropsUnknownBaseURLs guards the one store
// keyed by raw oba_base_url rather than by serverKey.
func TestRouteAgencyIndexPruneServersDropsUnknownBaseURLs(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://keep.example.com", map[string]string{"r1": "agency-a"})
	idx.Set("https://drop.example.com", map[string]string{"r2": "agency-b"})

	removed := idx.PruneServers(keepOnly("https://keep.example.com"))

	if len(removed) != 1 || removed[0] != "https://drop.example.com" {
		t.Fatalf("expected the stale base URL to be reported as removed, got %v", removed)
	}
	if _, ok := idx.Get("https://drop.example.com", "r2"); ok {
		t.Fatal("expected the stale route mapping to be gone")
	}
	if _, ok := idx.Get("https://keep.example.com", "r1"); !ok {
		t.Fatal("expected the configured route mapping to survive")
	}
}
