package geo

import (
	"fmt"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/golang/geo/s2"
)

const s2Level = 13 // S2 cell level with 850–1225 m spatial resolution

// NoStationID is the station_id reported for unmatched stops that do not belong
// to a GTFS station hierarchy. It is kept as a non-empty value so downstream
// queries can group on station_id without special-casing an empty label.
const NoStationID = "no_station"

// Cluster describes the spatial cluster an unmatched stop is reported under.
//
// The cluster is derived from the stop's own coordinates regardless of station
// membership, so a station can legitimately span several clusters when its
// platforms sit in different S2 cells — each (station_id, id) pair is reported
// separately. This matches how the GTFS hierarchy is validated: a stop that is
// part of a station (or is itself a station) is tagged with that station while
// still being clustered by its own location.
type Cluster struct {
	// ID is the S2 cluster identifier at s2Level for the stop's own coordinates,
	// e.g. "s2_...".
	ID string
	// StationID is the root station the stop belongs to, or NoStationID if the
	// stop is not part of a station hierarchy.
	StationID string
	// Latitude and Longitude are the center of the S2 cell in degrees, so the
	// cluster can be plotted on a map without decoding the ID.
	Latitude  float64
	Longitude float64
}

// s2CellID maps a latitude and longitude to the S2 CellID at the given level,
// which represents a region on the Earth's surface.
//
// S2 cells form a hierarchical decomposition of the sphere. Each level corresponds
// to a finer resolution. For example, level 14 corresponds to 600m-wide cells,
// and level 10 corresponds to 7–10km cells.
//
// Reference: Microsoft Docs on S2 cell levels and dimensions
// https://learn.microsoft.com/en-us/kusto/query/geo-point-to-s2cell-function
func s2CellID(lat, lon float64, level int) s2.CellID {
	ll := s2.LatLngFromDegrees(lat, lon)
	return s2.CellIDFromLatLng(ll).Parent(level)
}

func s2ClusterID(cellID s2.CellID) string {
	return fmt.Sprintf("s2_%d", uint64(cellID))
}

// s2CellCenterDegrees returns the center of the given S2 cell in degrees.
func s2CellCenterDegrees(cellID s2.CellID) (lat, lon float64) {
	center := s2.CellFromCellID(cellID).Center()
	ll := s2.LatLngFromPoint(center)
	return ll.Lat.Degrees(), ll.Lng.Degrees()
}

// getClusterID determines the cluster for a GTFS stop based on its location_type
// and its position in the parent_station hierarchy.
//
// The S2 cell is always derived from the stop's own latitude and longitude as
// long as it is not malformed, so no per-station API lookups are needed and the
// reported coordinates are guaranteed to reflect where the stop actually is.
//
// This function uses GTFS stop hierarchy rules as defined in the official GTFS specification:
// https://gtfs.org/documentation/schedule/reference/#stopstxt (see the `parent_station` section).
//
// It returns:
//   - cluster: the Cluster the stop is reported under, with the S2 cell of the
//     stop's own coordinates and its root station (or NoStationID).
//   - ok: false if the data is malformed or the stop has no coordinates.
//
// ---- Per location_type behavior ----
//
// location_type = 0 (Stop / Platform):
//   - The parent_station field is optional.
//   - If it has a parent_station (Type 1 Station), the stop is tagged with the station.
//   - Valid: platform with parent station (Type 1).
//   - Invalid: parent exists but is not of type 1.
//   - If it has no parent, the stop is tagged NoStationID.
//   - Coordinates are always required for the S2 cluster.
//
// location_type = 1 (Station):
//   - Always tagged by its own ID.
//   - Must not have a parent_station.
//   - Considered root of stop hierarchy.
//
// location_type = 2 or 3 (Entrance/Exit or Generic Node):
//   - Must have a parent_station of type 1 (Station).
//   - Valid: parent is station.
//   - Invalid: missing parent or parent not type 1, data is malformed.
//
// location_type = 4 (Boarding Area):
//   - Must have a parent of type 0 (Platform/Stop).
//   - Note: A Platform/Stop (type 0) may optionally have a parent of type 1 (Station)
//     if defined as part of a station hierarchy.
//   - Valid: parent is a Stop, and grandparent is a Station.
//   - Valid fallback: parent exists, but grandparent is missing - tagged NoStationID.
//   - Invalid: grandparent exists but is not a Station, or coordinates are missing - data is malformed.
//
// Returns false if hierarchy rules are violated or required coordinate data is missing.
func getClusterID(stop remoteGtfs.Stop) (Cluster, bool) {
	stationID := NoStationID

	switch stop.Type {
	case 0: // Stop or Platform
		if stop.Parent != nil {
			root := stop.Root()
			if root.Type != 1 {
				return Cluster{}, false // malformed hierarchy
			}
			stationID = root.Id
		}
	case 1: // Station
		// The root of a stop hierarchy is tagged by its own ID
		stationID = stop.Id
	case 2, 3: // Entrance/Exit or Generic Node
		if stop.Parent == nil || stop.Parent.Type != 1 {
			return Cluster{}, false // malformed hierarchy
		}
		stationID = stop.Parent.Id
	case 4: // Boarding Area
		if stop.Parent == nil || stop.Parent.Type != 0 {
			return Cluster{}, false // malformed hierarchy
		}
		grandparent := stop.Parent.Parent
		if grandparent != nil {
			if grandparent.Type != 1 {
				return Cluster{}, false // malformed hierarchy
			}
			stationID = grandparent.Id
		}
	default:
		return Cluster{}, false
	}

	if stop.Latitude == nil || stop.Longitude == nil {
		return Cluster{}, false
	}

	cellID := s2CellID(*stop.Latitude, *stop.Longitude, s2Level)
	lat, lon := s2CellCenterDegrees(cellID)
	return Cluster{
		ID:        s2ClusterID(cellID),
		StationID: stationID,
		Latitude:  lat,
		Longitude: lon,
	}, true
}
