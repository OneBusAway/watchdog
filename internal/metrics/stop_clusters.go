package metrics

import (
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
)

// ReportUnmatchedStopClusters uses hybrid clustering (station-based or S2) and exposes the metrics to Prometheus.
func ReportUnmatchedStopClusters(slugID, agencyID string, unmatchedStops map[string]gtfs.Stop) {
	clusterCount := make(map[string]int)
	clusterType := make(map[string]string) // station or s2

	for _, stop := range unmatchedStops {
		clusterID, ctype, ok := geo.GetClusterID(stop)
		if !ok {
			continue
		}
		clusterCount[clusterID]++
		clusterType[clusterID] = ctype
	}

	// Report each cluster to Prometheus
	for id, count := range clusterCount {
		UnmatchedStopClusterCount.WithLabelValues(slugID, agencyID, id, clusterType[id]).Set(float64(count))
	}
}
