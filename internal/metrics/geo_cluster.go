// internal/metrics/geo_cluster.go

package metrics

import (
	"fmt"

	"github.com/golang/geo/s2"
	"github.com/jamespfennell/gtfs"
)

const s2Level = 10 // ~600m spatial resolution

// s2ClusterID generates a stable S2-based cluster ID for a lat/lon.
func s2ClusterID(lat, lon float64, level int) string {
	ll := s2.LatLngFromDegrees(lat, lon)
	cellID := s2.CellIDFromLatLng(ll).Parent(level)
	return fmt.Sprintf("s2_%d", uint64(cellID))
}

// getClusterID determines the cluster ID and type based on the GTFS stop hierarchy and fallback logic.
// For more information about the hierarchy between different GTFS location types,
// refer to the `parent_station` section in the GTFS documentation:
// https://gtfs.org/schedule/reference/#stopstxt
// This function's logic follows the hierarchy defined in that specification.
func getClusterID(stop gtfs.Stop) (clusterID string, clusterType string, ok bool) {
	switch stop.Type {
	case 0: // Stop or Platform
		if stop.Parent != nil {
			root := stop.Root()
			if root.Type == 1 {
				return root.Id, "station", true
			}
			return "", "", false // malformed hierarchy
		} else if stop.Latitude != nil && stop.Longitude != nil {
			return s2ClusterID(*stop.Latitude, *stop.Longitude, s2Level), "s2", true
		}
	case 1: // Station
		// Cluster by its own ID since it's the root
		return stop.Id, "station", true
	case 2, 3: // Entrance/Exit or Generic Node
		if stop.Parent != nil && stop.Parent.Type == 1 {
			return stop.Parent.Id, "station", true
		}
	case 4: // Boarding Area
		if stop.Parent != nil && stop.Parent.Type == 0 {
			grandparent := stop.Parent.Parent
			if grandparent == nil {
				if stop.Latitude != nil && stop.Longitude != nil {
					return s2ClusterID(*stop.Latitude, *stop.Longitude, s2Level), "s2", true
				}
				return "", "", false
			}
			if grandparent.Type == 1 {
				return grandparent.Id, "station", true
			}
			// malformed if grandparent exists but not a station
			return "", "", false
		}
	}
	return "", "", false
}

// ReportUnmatchedStopClusters uses hybrid clustering (station-based or S2) and exposes the metrics to Prometheus.
func ReportUnmatchedStopClusters(slugID, agencyID string, unmatchedStops map[string]gtfs.Stop) {
	clusterCount := make(map[string]int)
	clusterType := make(map[string]string) // station or s2

	for _, stop := range unmatchedStops {
		clusterID, ctype, ok := getClusterID(stop)
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
