package metrics

import (
	"context"
	"testing"
	"time"
)

func TestUnmatchedStopTrackerTracksByAgency(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen("agency-a", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordClusterSeen("agency-a", "station-1", "s2_111", "1.100000", "2.200000")
	tracker.RecordLastSeen("agency-b", "stop-1", "Stop One", "1.100000", "2.200000")

	if len(tracker.Entries) != 2 || len(tracker.Entries["agency-a"]) != 1 {
		t.Fatalf("tracker did not isolate agencies: %+v", tracker.Entries)
	}
	if len(tracker.Clusters["agency-a"]) != 1 {
		t.Fatalf("expected one cluster: %+v", tracker.Clusters)
	}
}

func TestUnmatchedStopTrackerClearRoutineCancels(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { tracker.ClearRoutine(ctx, time.Millisecond, time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ClearRoutine did not stop on context cancellation")
	}
}
