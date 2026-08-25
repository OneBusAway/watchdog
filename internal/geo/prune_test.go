package geo

import "testing"

func TestBoundingBoxStorePruneDropsUnknownKeys(t *testing.T) {
	store := NewBoundingBoxStore()
	store.Set("keep|agency-a", BoundingBox{MinLat: 1, MaxLat: 2, MinLon: 3, MaxLon: 4})
	store.Set("drop|agency-b", BoundingBox{MinLat: 5, MaxLat: 6, MinLon: 7, MaxLon: 8})

	removed := store.Prune(func(key string) bool { return key == "keep|agency-a" })

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed, got %v", removed)
	}
	if _, ok := store.Get("drop|agency-b"); ok {
		t.Fatal("expected the stale bounding box to be gone")
	}
	if _, ok := store.Get("keep|agency-a"); !ok {
		t.Fatal("expected the configured bounding box to survive")
	}
}
