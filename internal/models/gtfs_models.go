package models

import (
	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

// StaticData represents the static GTFS data structure.
// It contains parts we use from GTFS static bundles
// (stops, agencies, services, and routes).
//
// Routes are kept so the route and trip attribution index can be populated when
// the bundle is parsed. In agency-mode this is an owned snapshot containing only
// the configured agency's routes, services, and referenced stops. In server-mode
// it remains the consolidated, shared static bundle.
// IMPORTANT:
// In the future, we may need to extend this structure
// to include more fields from the GTFS Static bundle.
// Don't forget to include them here
type StaticData struct {
	Stops    []remoteGtfs.Stop
	Agencies []remoteGtfs.Agency
	Services []remoteGtfs.Service
	Routes   []remoteGtfs.Route
}

// NewStaticData copies the portions of a parsed GTFS bundle used by Watchdog.
func NewStaticData(GtfsStaticBundle *remoteGtfs.Static) *StaticData {
	return &StaticData{
		Stops:    append([]remoteGtfs.Stop(nil), GtfsStaticBundle.Stops...),
		Agencies: append([]remoteGtfs.Agency(nil), GtfsStaticBundle.Agencies...),
		Services: append([]remoteGtfs.Service(nil), GtfsStaticBundle.Services...),
		Routes:   append([]remoteGtfs.Route(nil), GtfsStaticBundle.Routes...),
	}
}

// RealtimeVehicle carries one GTFS-RT vehicle position together with the
// identity of the feed it came from.
//
// GTFS-RT vehicle IDs are only unique within a single feed. A deployment may
// scrape multiple feeds whose vehicles all belong to the same umbrella agency,
// and two feeds can legitimately reuse the same numeric vehicle ID for
// different physical vehicles. FeedID records which feed a vehicle was
// observed in so downstream consumers can key per-vehicle identity on the
// (feed, vehicle_id) pair instead of the vehicle ID alone.
type RealtimeVehicle struct {
	Vehicle remoteGtfs.Vehicle
	FeedID  string
}

// RealtimeData represents the realtime GTFS data structure.
// It contains parts we uses from GTFS Realtime bundels
// which are vehicles.
// IMPORTANT:
// In the future, we may need to extend this structure
// to include more fields from the GTFS Realtime bundle.
// Don't forget to include them here
type RealtimeData struct {
	Vehicles []RealtimeVehicle
}

// NewRealtimeData wraps every vehicle in a bundle with an empty FeedID. It is
// used by code paths that work with a single (already feed-scoped) bundle;
// multi-feed fetching stamps the FeedID explicitly.
func NewRealtimeData(GtfsRealtimeBundle *remoteGtfs.Realtime) *RealtimeData {
	vehicles := make([]RealtimeVehicle, 0, len(GtfsRealtimeBundle.Vehicles))
	for _, vehicle := range GtfsRealtimeBundle.Vehicles {
		vehicles = append(vehicles, RealtimeVehicle{Vehicle: vehicle})
	}
	return &RealtimeData{Vehicles: vehicles}
}
