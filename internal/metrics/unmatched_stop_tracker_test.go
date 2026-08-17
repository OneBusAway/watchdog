package metrics

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestUnmatchedStopTrackerTracksByAgency(t *testing.T) {
	tracker := NewUnmatchedStopTracker()
	tracker.RecordLastSeen("agency-a", "Agency A", "stop-1", "Stop One", "1.100000", "2.200000")
	tracker.RecordClusterSeen("agency-a", "Agency A", "station-1", "s2_111", "1.100000", "2.200000")
	tracker.RecordLastSeen("agency-b", "Agency B", "stop-1", "Stop One", "1.100000", "2.200000")

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

// collectStopSeries gathers the label sets currently exposed by
// ObaUnmatchedStopInfo, sorted for stable comparison.
func collectStopSeries(t *testing.T) []map[string]string {
	t.Helper()
	c := make(chan prometheus.Metric, 32)
	ObaUnmatchedStopInfo.Collect(c)
	close(c)

	var series []map[string]string
	for m := range c {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		labels := make(map[string]string)
		for _, lp := range pb.Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		series = append(series, labels)
	}

	sort.Slice(series, func(i, j int) bool {
		return stopSeriesKey(series[i]) < stopSeriesKey(series[j])
	})
	return series
}

func stopSeriesKey(labels map[string]string) string {
	return labels["agency_id"] + "|" + labels["stop_id"] + "|" + labels["stop_name"] + "|" + labels["lat"] + "|" + labels["lon"]
}

func findStopSeries(series []map[string]string, agencyID, stopID, stopName, lat, lon string) map[string]string {
	for _, labels := range series {
		if labels["agency_id"] == agencyID && labels["stop_id"] == stopID &&
			labels["stop_name"] == stopName && labels["lat"] == lat && labels["lon"] == lon {
			return labels
		}
	}
	return nil
}

func TestRecordLastSeenUpdatesLabelsOnRename(t *testing.T) {
	const (
		agencyID = "rename-test-agency"
		stopID   = "stop-moving"
	)
	tracker := NewUnmatchedStopTracker()

	ObaUnmatchedStopInfo.WithLabelValues(agencyID, "Rename Agency", stopID, "Old Name", "1.100000", "2.200000").Set(1)
	tracker.RecordLastSeen(agencyID, "Rename Agency", stopID, "Old Name", "1.100000", "2.200000")

	ObaUnmatchedStopInfo.WithLabelValues(agencyID, "Rename Agency", stopID, "New Name", "3.300000", "4.400000").Set(1)
	tracker.RecordLastSeen(agencyID, "Rename Agency", stopID, "New Name", "3.300000", "4.400000")

	entry := tracker.Entries[agencyID][stopID]
	if entry.StopName != "New Name" || entry.Lat != "3.300000" || entry.Lon != "4.400000" {
		t.Fatalf("tracker froze first-seen labels, got %+v", entry)
	}

	series := collectStopSeries(t)
	if findStopSeries(series, agencyID, stopID, "Old Name", "1.100000", "2.200000") != nil {
		t.Fatalf("old name series was not pruned on rename: %+v", series)
	}
	if findStopSeries(series, agencyID, stopID, "New Name", "3.300000", "4.400000") == nil {
		t.Fatalf("new name series not present after rename: %+v", series)
	}
}

func TestClearStopsPrunesRenamedStopOnStale(t *testing.T) {
	const (
		agencyID = "rename-clear-test-agency"
		stopID   = "stop-renamed"
	)
	tracker := NewUnmatchedStopTracker()

	ObaUnmatchedStopInfo.WithLabelValues(agencyID, "Rename Clear Agency", stopID, "Very Old Name", "1.100000", "2.200000").Set(1)
	tracker.RecordLastSeen(agencyID, "Rename Clear Agency", stopID, "Very Old Name", "1.100000", "2.200000")

	ObaUnmatchedStopInfo.WithLabelValues(agencyID, "Rename Clear Agency", stopID, "Latest Name", "9.900000", "8.800000").Set(1)
	tracker.RecordLastSeen(agencyID, "Rename Clear Agency", stopID, "Latest Name", "9.900000", "8.800000")

	entry := tracker.Entries[agencyID][stopID]
	entry.LastSeen = time.Now().UTC().Add(-48 * time.Hour)
	tracker.Entries[agencyID][stopID] = entry

	tracker.clear(24 * time.Hour)

	series := collectStopSeries(t)
	if findStopSeries(series, agencyID, stopID, "Very Old Name", "1.100000", "2.200000") != nil {
		t.Fatalf("old name series survived after stale clear: %+v", series)
	}
	if findStopSeries(series, agencyID, stopID, "Latest Name", "9.900000", "8.800000") != nil {
		t.Fatalf("latest name series survived after stale clear: %+v", series)
	}
}
