package metrics

import "testing"

// TestUnmatchedStopTrackerPruneDropsUnknownKeys covers the leak: entries for a
// server that has left the configuration linger until their own 24h TTL
// expires, even though the server will never report again.
func TestUnmatchedStopTrackerPruneDropsUnknownKeys(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen("keep|agency-a", "agency-a", "Agency A", "keep", "https://keep.example.com", "stop-1", "Stop 1", "1.000000", "2.000000")
	tracker.RecordClusterSeen("keep|agency-a", "agency-a", "Agency A", "keep", "https://keep.example.com", "station-1", "cluster-1", "1.000000", "2.000000")
	tracker.RecordLastSeen("drop|agency-b", "agency-b", "Agency B", "drop", "https://drop.example.com", "stop-2", "Stop 2", "3.000000", "4.000000")
	tracker.RecordClusterSeen("drop|agency-b", "agency-b", "Agency B", "drop", "https://drop.example.com", "station-2", "cluster-2", "3.000000", "4.000000")

	removed := tracker.Prune(func(key string) bool { return key == "keep|agency-a" })

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed exactly once, got %v", removed)
	}
	if _, ok := tracker.Entries["drop|agency-b"]; ok {
		t.Fatal("expected the stale tracked stops to be gone")
	}
	if _, ok := tracker.Clusters["drop|agency-b"]; ok {
		t.Fatal("expected the stale tracked clusters to be gone")
	}
	if _, ok := tracker.Entries["keep|agency-a"]; !ok {
		t.Fatal("expected the configured server's tracked stops to survive")
	}
	if _, ok := tracker.Clusters["keep|agency-a"]; !ok {
		t.Fatal("expected the configured server's tracked clusters to survive")
	}
}

// TestUnmatchedStopTrackerPruneDropsClusterOnlyKeys guards the case the stop
// map alone would miss: clearStops deletes a server's (now empty) stop map
// while its clusters are still within the TTL, so a departed server can be
// present in Clusters and absent from Entries.
func TestUnmatchedStopTrackerPruneDropsClusterOnlyKeys(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordClusterSeen("drop|agency-b", "agency-b", "Agency B", "drop", "https://drop.example.com", "station-2", "cluster-2", "3.000000", "4.000000")

	removed := tracker.Prune(func(string) bool { return false })

	if len(removed) != 1 || removed[0] != "drop|agency-b" {
		t.Fatalf("expected drop|agency-b to be reported as removed, got %v", removed)
	}
	if _, ok := tracker.Clusters["drop|agency-b"]; ok {
		t.Fatal("expected the stale tracked clusters to be gone")
	}
}
