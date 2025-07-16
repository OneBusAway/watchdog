package geo

import (
	"fmt"

	"github.com/golang/geo/s2"
	"github.com/jamespfennell/gtfs"
)

const s2Level = 13 // S2 cell level with 850–1225 m spatial resolution

// s2ClusterID returns a stable cluster ID based on the S2 geometry library.
// It maps a latitude and longitude to the S2 CellID at the given level,
// which represents a region on the Earth's surface.
//
// S2 cells form a hierarchical decomposition of the sphere. Each level corresponds
// to a finer resolution. For example, level 14 corresponds to 600m-wide cells,
// and level 10 corresponds to 7–10km cells.
//
// Reference: Microsoft Docs on S2 cell levels and dimensions
// https://learn.microsoft.com/en-us/kusto/query/geo-point-to-s2cell-function
func s2ClusterID(lat, lon float64, level int) string {
	ll := s2.LatLngFromDegrees(lat, lon)
	cellID := s2.CellIDFromLatLng(ll).Parent(level)
	return fmt.Sprintf("s2_%d", uint64(cellID))
}

// GetClusterID determines the cluster ID and type based on the GTFS stop hierarchy and fallback logic.
// For more information about the hierarchy between different GTFS location types,
// refer to the `parent_station` section in the GTFS documentation:
// https://gtfs.org/schedule/reference/#stopstxt
// This function's logic follows the hierarchy defined in that specification.
func GetClusterID(stop gtfs.Stop) (clusterID string, clusterType string, ok bool) {
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

