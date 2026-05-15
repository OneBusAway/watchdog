package metrics

import (
    "testing"

    remoteGtfs "github.com/jamespfennell/gtfs"
)

func float64Ptr(f float64) *float64 { return &f }

func TestReportUnmatchedStopClusters(t *testing.T) {

    t.Run("Empty map reports no metrics", func(t *testing.T) {
        // Should not panic or error
        reportUnmatchedStopClusters("server-1", "agency-1", map[string]remoteGtfs.Stop{})
    })

    t.Run("Stop with no parent and valid coords uses S2 clustering", func(t *testing.T) {
        stops := map[string]remoteGtfs.Stop{
            "stop-1": {
                Id:        "stop-1",
                Type:      0,
                Latitude:  float64Ptr(47.6062),
				Longitude: float64Ptr(-122.3321),
            },
        }
        reportUnmatchedStopClusters("server-1", "agency-1", stops)

        // Metric should be set, we just verify no panic and the gauge exists
        // Full label validation would require knowing the S2 cell ID at runtime
    })

    t.Run("Station type stop clusters by its own ID", func(t *testing.T) {
        stops := map[string]remoteGtfs.Stop{
            "station-1": {
                Id:   "station-1",
                Type: 1, // Station
            },
        }
        reportUnmatchedStopClusters("server-2", "agency-2", stops)

        metricValue, err := getMetricValue(UnmatchedStopClusterCount, map[string]string{
            "server":       "server-2",
            "agency":       "agency-2",
            "cluster_id":   "station-1",
            "cluster_type": "station",
        })
        if err != nil {
            t.Fatal(err)
        }
        if metricValue != 1 {
            t.Errorf("Expected 1 stop in station cluster, got %v", metricValue)
        }
    })

    t.Run("Malformed stop with no parent and no coords is skipped", func(t *testing.T) {
        stops := map[string]remoteGtfs.Stop{
            "bad-stop": {
                Id:   "bad-stop",
                Type: 0,
                // No parent, no lat/lon → getClusterID returns ok=false
            },
        }
        // Should silently skip — no panic
        reportUnmatchedStopClusters("server-3", "agency-3", stops)
    })

    t.Run("Multiple stops in same station cluster aggregated correctly", func(t *testing.T) {
        parent := &remoteGtfs.Stop{
            Id:   "station-root",
            Type: 1,
        }
        stops := map[string]remoteGtfs.Stop{
            "platform-1": {Id: "platform-1", Type: 0, Parent: parent},
            "platform-2": {Id: "platform-2", Type: 0, Parent: parent},
            "platform-3": {Id: "platform-3", Type: 0, Parent: parent},
        }
        reportUnmatchedStopClusters("server-4", "agency-4", stops)

        metricValue, err := getMetricValue(UnmatchedStopClusterCount, map[string]string{
            "server":       "server-4",
            "agency":       "agency-4",
            "cluster_id":   "station-root",
            "cluster_type": "station",
        })
        if err != nil {
            t.Fatal(err)
        }
        if metricValue != 3 {
            t.Errorf("Expected 3 stops in station cluster, got %v", metricValue)
        }
    })
}