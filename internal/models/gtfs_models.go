package models

import (
	"time"

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
// FeedID remains "0" for the single configured source so existing metric
// labels and internal freshness keys remain compatible. Vehicle IDs are the
// final IDs from the upstream feed.
type RealtimeVehicle struct {
	Vehicle   remoteGtfs.Vehicle
	FeedID    string
	StateHash string
}

// RealtimeFeed describes the accepted upstream realtime
// data. FullDataset controls safe retirement of absent vehicle series.
type RealtimeFeed struct {
	FeedID      string
	FeedURL     string
	FullDataset bool
}

// RealtimeData represents the realtime GTFS data structure.
// It contains parts we uses from GTFS Realtime bundels
// which are vehicles.
// IMPORTANT:
// In the future, we may need to extend this structure
// to include more fields from the GTFS Realtime bundle.
// Don't forget to include them here
type RealtimeData struct {
	Vehicles      []RealtimeVehicle
	Feeds         []RealtimeFeed
	ObservationAt time.Time
}

// NewRealtimeData wraps every vehicle in a bundle with an empty FeedID. It is
// used by code paths that work with a single (already feed-scoped) bundle;
// the fetch path stamps the compatible feed label "0" explicitly.
func NewRealtimeData(GtfsRealtimeBundle *remoteGtfs.Realtime) *RealtimeData {
	vehicles := make([]RealtimeVehicle, 0, len(GtfsRealtimeBundle.Vehicles))
	for _, vehicle := range GtfsRealtimeBundle.Vehicles {
		vehicles = append(vehicles, RealtimeVehicle{Vehicle: vehicle})
	}
	return &RealtimeData{Vehicles: vehicles}
}
