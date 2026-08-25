package metrics

import "testing"

func TestVehicleLastSeenPruneDropsUnknownKeys(t *testing.T) {
	store := NewVehicleLastSeen()
	store.Set("keep|agency-a", "0", "va", LastSeen{Lat: 1, Lon: 2})
	store.Set("drop|agency-b", "0", "vb", LastSeen{Lat: 3, Lon: 4})

	removed := store.Prune(func(key string) bool { return key == "keep|agency-a" })

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed, got %v", removed)
	}
	if store.Count("drop|agency-b") != 0 {
		t.Fatal("expected the stale vehicles to be gone")
	}
	if store.Count("keep|agency-a") != 1 {
		t.Fatal("expected the configured vehicles to survive")
	}
}
