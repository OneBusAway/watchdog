package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUnmatchedStopTrackerRecordLastSeen(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-2", "Stop Two", "3.300000", "4.400000")

	tracker.Mu.RLock()
	defer tracker.Mu.RUnlock()
	if len(tracker.Entries) != 1 {
		t.Fatalf("expected 1 server entry, got %d", len(tracker.Entries))
	}
	stops := tracker.Entries[1]["agency-x"]
	if len(stops) != 2 {
		t.Fatalf("expected 2 stops, got %d", len(stops))
	}
	if entry := stops["stop-1"]; entry.Slug != "slug-a" || entry.StopName != "Stop One" {
		t.Fatalf("unexpected stop entry: %+v", entry)
	}
}

func TestUnmatchedStopTrackerRecordClusterSeen(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "station-1", "station")
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "station-1", "station")
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "s2-abc", "s2")

	tracker.Mu.RLock()
	defer tracker.Mu.RUnlock()
	if len(tracker.Clusters) != 1 {
		t.Fatalf("expected 1 server entry, got %d", len(tracker.Clusters))
	}
	clusters := tracker.Clusters[1]["agency-x"]
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	if entry, ok := clusters[clusterKey{ID: "s2-abc", Type: "s2"}]; !ok || entry.Agency != "agency-x" {
		t.Fatalf("unexpected cluster entry: %+v", entry)
	}
}

func TestUnmatchedStopTrackerClearStaleStopsAndClusters(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	ObaUnmatchedStopInfo.WithLabelValues("slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000").Set(1)
	UnmatchedStopClusterCount.WithLabelValues("slug-a", "agency-x", "station-1", "station").Set(3)

	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "station-1", "station")

	tracker.Mu.Lock()
	tracker.Entries[1]["agency-x"]["stop-1"] = withLastSeen(tracker.Entries[1]["agency-x"]["stop-1"], time.Now().Add(-2*24*time.Hour))
	tracker.Clusters[1]["agency-x"][clusterKey{ID: "station-1", Type: "station"}] = withClusterLastSeen(tracker.Clusters[1]["agency-x"][clusterKey{ID: "station-1", Type: "station"}], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	stopAgencies := tracker.Entries[1]
	clusterAgencies := tracker.Clusters[1]
	tracker.Mu.RUnlock()
	if len(stopAgencies) != 0 {
		t.Fatalf("expected stop entries cleared, got %d", len(stopAgencies))
	}
	if len(clusterAgencies) != 0 {
		t.Fatalf("expected cluster entries cleared, got %d", len(clusterAgencies))
	}

	if count := testutil.CollectAndCount(ObaUnmatchedStopInfo); count != 0 {
		t.Errorf("expected no oba_unmatched_stop_info series after clear, got %d", count)
	}
	if count := testutil.CollectAndCount(UnmatchedStopClusterCount); count != 0 {
		t.Errorf("expected no oba_unmatched_stop_cluster_count series after clear, got %d", count)
	}
}

func TestUnmatchedStopTrackerClearKeepsFresh(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "station-1", "station")

	tracker.Mu.Lock()
	tracker.Entries[1]["agency-x"]["stop-1"] = withLastSeen(tracker.Entries[1]["agency-x"]["stop-1"], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	stops := tracker.Entries[1]["agency-x"]
	clusters := tracker.Clusters[1]["agency-x"]
	tracker.Mu.RUnlock()
	if _, ok := stops["stop-1"]; ok {
		t.Fatal("expected stale stop-1 to be cleared")
	}
	if _, ok := clusters[clusterKey{ID: "station-1", Type: "station"}]; !ok {
		t.Fatal("expected fresh cluster to be retained")
	}
}

func TestUnmatchedStopTrackerClearReclusteringGhost(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	UnmatchedStopClusterCount.WithLabelValues("slug-a", "agency-x", "old-cluster", "s2").Set(4)

	// First tick: cluster reported as old-cluster/s2.
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "old-cluster", "s2")
	tracker.Mu.Lock()
	tracker.Clusters[1]["agency-x"][clusterKey{ID: "old-cluster", Type: "s2"}] = withClusterLastSeen(tracker.Clusters[1]["agency-x"][clusterKey{ID: "old-cluster", Type: "s2"}], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	// Second tick: the stops re-clustered; only new-cluster is reported.
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "new-cluster", "station")

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	clusters := tracker.Clusters[1]["agency-x"]
	tracker.Mu.RUnlock()
	if _, ok := clusters[clusterKey{ID: "old-cluster", Type: "s2"}]; ok {
		t.Fatal("expected re-clustered old-cluster entry to be cleared")
	}
	if _, ok := clusters[clusterKey{ID: "new-cluster", Type: "station"}]; !ok {
		t.Fatal("expected new-cluster entry to be retained")
	}

	if count := testutil.CollectAndCount(UnmatchedStopClusterCount); count != 0 {
		t.Errorf("expected stale old-cluster series to be deleted, got %d series", count)
	}
}

func TestUnmatchedStopTrackerClearClusterWithoutStops(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	UnmatchedStopClusterCount.WithLabelValues("slug-a", "agency-x", "station-1", "station").Set(2)

	// Cluster is reported but none of its stops get individual stop entries.
	tracker.RecordClusterSeen(1, "slug-a", "agency-x", "station-1", "station")
	tracker.Mu.Lock()
	tracker.Clusters[1]["agency-x"][clusterKey{ID: "station-1", Type: "station"}] = withClusterLastSeen(tracker.Clusters[1]["agency-x"][clusterKey{ID: "station-1", Type: "station"}], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	clusters := tracker.Clusters[1]
	tracker.Mu.RUnlock()
	if len(clusters) != 0 {
		t.Fatalf("expected cluster entries cleared, got %d", len(clusters))
	}

	if count := testutil.CollectAndCount(UnmatchedStopClusterCount); count != 0 {
		t.Errorf("expected no oba_unmatched_stop_cluster_count series after clear, got %d", count)
	}
}

func TestUnmatchedStopTrackerClearRoutineCancels(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		tracker.ClearRoutine(ctx, time.Millisecond, time.Hour)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ClearRoutine did not stop on context cancellation")
	}
}

func withLastSeen(entry trackedStop, ts time.Time) trackedStop {
	entry.LastSeen = ts
	return entry
}

func withClusterLastSeen(entry trackedCluster, ts time.Time) trackedCluster {
	entry.LastSeen = ts
	return entry
}
