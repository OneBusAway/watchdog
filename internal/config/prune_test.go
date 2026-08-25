package config

import "testing"

// TestBackoffStorePruneDropsUnknownKeys covers the leak: a server removed from
// the configuration keeps its backoff state for the life of the process. The
// leak also has a correctness edge — if that server is later re-added it
// inherits the stale NextRetryAt and is skipped for collection on its first
// ticks even though nothing has failed yet.
func TestBackoffStorePruneDropsUnknownKeys(t *testing.T) {
	store := NewBackoffStore()
	store.UpdateBackoff("keep|agency-a")
	store.UpdateBackoff("drop|agency-b")

	removed := store.Prune(func(key string) bool { return key == "keep|agency-a" })

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed, got %v", removed)
	}
	if _, ok := store.NextRetryAt("drop|agency-b"); ok {
		t.Fatal("expected the stale backoff state to be gone")
	}
	if _, ok := store.NextRetryAt("keep|agency-a"); !ok {
		t.Fatal("expected the configured server's backoff state to survive")
	}
}
