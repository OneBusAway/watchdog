package models

import (
	"crypto/sha256"
	"encoding/hex"
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
// SourceTimestamp is the FeedHeader timestamp, nil when the header has none.
// PayloadHash is the canonical entity hash, which excludes the FeedHeader.
// The fetch time is RealtimeData.ObservationAt.
//
// SourceKey is FeedSourceKey of the configured feed URL. Feeds that differ
// only by query string share FeedURL, which drops the query, but not
// SourceKey.
//
// ReportedVehicleIDs holds the ID of every vehicle position entity in the
// response, duplicates included. Agency-mode filtering removes vehicles from
// RealtimeData.Vehicles but never from this set, so it says whether a vehicle
// was in the response at all.
type RealtimeFeed struct {
	FeedID             string
	FeedURL            string
	FullDataset        bool
	SourceTimestamp    *time.Time
	PayloadHash        string
	SourceKey          string
	ReportedVehicleIDs map[string]struct{}
}

// FeedSourceKey identifies a realtime feed by its full configured URL, query
// string included. It is a hash because the query can carry a credential:
// hold it in memory only, and never log or store it.
func FeedSourceKey(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
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
