package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUnmatchedStopTrackerRecordLastSeen(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000", "", "", false)
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000", "", "", false)
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-2", "Stop Two", "3.300000", "4.400000", "cluster-1", "s2", true)

	tracker.Mu.RLock()
	if len(tracker.Entries) != 1 {
		t.Fatalf("expected 1 server entry, got %d", len(tracker.Entries))
	}
	stops := tracker.Entries[1]["agency-x"]
	if len(stops) != 2 {
		t.Fatalf("expected 2 stops, got %d", len(stops))
	}
	if entry := stops["stop-2"]; !entry.HasCluster || entry.ClusterID != "cluster-1" || entry.ClusterType != "s2" {
		t.Fatalf("unexpected cluster metadata: %+v", entry)
	}
	if entry := stops["stop-1"]; entry.HasCluster {
		t.Fatalf("stop-1 should not have cluster metadata: %+v", entry)
	}
	tracker.Mu.RUnlock()
}

func TestUnmatchedStopTrackerClear(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	ObaUnmatchedStopInfo.WithLabelValues("slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000").Set(1)
	UnmatchedStopClusterCount.WithLabelValues("slug-a", "agency-x", "cluster-1", "s2").Set(2)

	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000", "", "", false)
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-2", "Stop Two", "3.300000", "4.400000", "cluster-1", "s2", true)

	tracker.Mu.Lock()
	tracker.Entries[1]["agency-x"]["stop-1"] = withLastSeen(tracker.Entries[1]["agency-x"]["stop-1"], time.Now().Add(-2*24*time.Hour))
	tracker.Entries[1]["agency-x"]["stop-2"] = withLastSeen(tracker.Entries[1]["agency-x"]["stop-2"], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	agencies := tracker.Entries[1]
	tracker.Mu.RUnlock()
	if len(agencies) != 0 {
		t.Fatalf("expected all entries cleared, got %d", len(agencies))
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
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-1", "Stop One", "1.100000", "2.200000", "", "", false)
	tracker.RecordLastSeen(1, "slug-a", "agency-x", "stop-2", "Stop Two", "3.300000", "4.400000", "", "", false)

	tracker.Mu.Lock()
	tracker.Entries[1]["agency-x"]["stop-2"] = withLastSeen(tracker.Entries[1]["agency-x"]["stop-2"], time.Now().Add(-2*24*time.Hour))
	tracker.Mu.Unlock()

	tracker.clear(24 * time.Hour)

	tracker.Mu.RLock()
	stops := tracker.Entries[1]["agency-x"]
	tracker.Mu.RUnlock()
	if _, ok := stops["stop-1"]; !ok {
		t.Fatal("expected fresh stop-1 to be retained")
	}
	if _, ok := stops["stop-2"]; ok {
		t.Fatal("expected stale stop-2 to be cleared")
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
