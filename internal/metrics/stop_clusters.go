package metrics

import (
	remoteGtfs "github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
)

// reportUnmatchedStopClusters groups unmatched GTFS stops using hybrid clustering
// (station-based or S2-based) and reports the cluster counts as Prometheus metrics.
//
// Clustering logic:
// - If a stop belongs to a station hierarchy, it is grouped under the root station ID.
// - Otherwise, the stop is assigned to a geographic S2 cluster based on its lat/lon.
//
// Reported metric:
// - UnmatchedStopClusterCount: labeled by slug ID, agency ID, cluster ID, and cluster type ("station" or "s2").
//
// Parameters:
// - slugID: a unique identifier for the server or deployment instance
// - agencyID: the GTFS agency identifier
// - unmatchedStops: a map of stop IDs to GTFS stop objects not matched to gtfs static data
func reportUnmatchedStopClusters(slugID, agencyID string, unmatchedStops map[string]remoteGtfs.Stop) {
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
