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

// GetClusterID determines the cluster ID and type for a GTFS stop based on its location_type
// and its position in the parent_station hierarchy.
//
// This function uses GTFS stop hierarchy rules as defined in the official GTFS specification:
// https://gtfs.org/documentation/schedule/reference/#stopstxt (see the `parent_station` section).
//
// It returns:
//   - clusterID: the ID of the clustering entity (station ID or generated S2 ID).
//   - clusterType: either "station" or "s2".
//   - ok: false if the data is malformed or clustering could not be determined.
//
// ---- Per location_type behavior ----
//
// location_type = 0 (Stop / Platform):
//   - The parent_station field is optional.
//   - If it has a parent_station (Type 1 Station), the stop is clustered by the parent station's ID.
//     - Valid: platform with parent station (Type 1).
//     - Invalid: parent exists but is not of type 1.
//   - If it has no parent but has lat/lon, cluster by S2 cell.
//   - If it has no parent and no coordinates, data is malformed.
//
// location_type = 1 (Station):
//   - Always clustered by its own ID.
//   - Must not have a parent_station.
//   - Considered root of stop hierarchy.
//
// location_type = 2 or 3 (Entrance/Exit or Generic Node):
//   - Must have a parent_station of type 1 (Station).
//     - Valid: parent is station.
//     - Invalid: missing parent or parent not type 1, data is malformed.
//
// location_type = 4 (Boarding Area):
//   - Must have a parent of type 0 (Platform/Stop).
//   - Note: A Platform/Stop (type 0) may optionally have a parent of type 1 (Station)
//           if defined as part of a station hierarchy.
//     - Valid: parent is a Stop, and grandparent is a Station.
//     - Valid fallback: parent exists, but grandparent is missing - cluster by S2 using the stop's lat/lon.
//     - Invalid: grandparent exists but is not a Station, or coordinates are missing for fallback - data is malformed.
//
// Returns false if hierarchy rules are violated or required parent/coordinate data is missing.
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

