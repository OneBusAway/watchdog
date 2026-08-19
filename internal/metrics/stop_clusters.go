package metrics

import (
	"fmt"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/geo"
)

// reportUnmatchedStopClusters groups unmatched GTFS stops into spatial S2
// clusters (always based on the stop's own coordinates at level 13) and reports
// the cluster counts as Prometheus metrics.
//
// Clustering logic:
//   - Each valid stop is assigned to the S2 cell containing its own lat/lon.
//   - Stops that are part of a station hierarchy (or are themselves stations) are
//     additionally tagged with the root station ID; otherwise the station_id label
//     is geo.NoStationID. Grouping is by the (station_id, cluster_id) pair so two
//     stations sharing an S2 cell are not merged.
//
// Reported metric:
//   - UnmatchedStopClusterCount: labeled by agency ID, station ID, S2 cluster ID,
//     and the cluster's latitude/longitude.
//
// Parameters:
//   - serverKey: the composite server key (oba_base_url + agency_id) used to key
//     the tracker's outer map
//   - agencyID: the GTFS agency identifier
//   - agencyName: the human-readable server/agency name, used as a metric label
//   - unmatchedStops: a map of stop IDs to GTFS stop objects not matched to gtfs static data
//   - tracker: used to record cluster observations so stale cluster series can be cleaned up later.
func reportUnmatchedStopClusters(serverKey, agencyID, agencyName string, unmatchedStops map[string]remoteGtfs.Stop, tracker *UnmatchedStopTracker) {
	type clusterKey struct {
		stationID string
		clusterID string
	}
	clusterCount := make(map[clusterKey]int)
	clusterLocation := make(map[clusterKey][2]string) // [lat, lon]

	for _, stop := range unmatchedStops {
		cluster, ok := geo.GetClusterID(stop)
		if !ok {
			continue
		}
		key := clusterKey{stationID: cluster.StationID, clusterID: cluster.ID}
		clusterCount[key]++
		clusterLocation[key] = [2]string{
			fmt.Sprintf("%.6f", cluster.Latitude),
			fmt.Sprintf("%.6f", cluster.Longitude),
		}
	}

	// Report each cluster to Prometheus and record its observation so the
	// cluster series can be pruned once the cluster stops appearing.
	for key, count := range clusterCount {
		lat, lon := clusterLocation[key][0], clusterLocation[key][1]
		tracker.RecordClusterSeen(serverKey, agencyID, agencyName, key.stationID, key.clusterID, lat, lon)
		UnmatchedStopClusterCount.WithLabelValues(agencyID, agencyName, key.stationID, key.clusterID, lat, lon).Set(float64(count))
	}
}
