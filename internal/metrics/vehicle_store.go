package metrics

import (
	"context"
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// LastSeen stores timestamp & coordinates for speed computation
type LastSeen struct {
	Time              time.Time
	SourceTimestamp   time.Time
	ObservedAt        time.Time
	Lat               float64
	Lon               float64
	HasPosition       bool
	StateHash         string
	StateLastChanged  time.Time
	SourceInterval    time.Duration
	SpeedLastComputed time.Time
	VehicleID         string
	FeedID            string
	AgencyID          string
	AgencyName        string
	ServerName        string
	ServerURL         string
}

// vehicleKey composes the inner store key from the feed identity and vehicle
// ID. GTFS-RT vehicle IDs are only unique within a single feed, so the feed
// identity is part of the key: two different feeds that reuse the same vehicle
// ID for different physical vehicles stay separate.
func vehicleKey(feedID, vehicleID string) string {
	return feedID + "|" + vehicleID
}

// VehicleLastSeen stores the most recent known location and timestamp for each vehicle per server.
//
// The outer map key is the composite server key (oba_base_url + agency_id), and the
// inner map key is the feed identity plus vehicle ID (feedID|vehicleID).
// Each entry stores a `LastSeen` struct containing the last known latitude, longitude, and timestamp.
//
// This cache is used to:
//   - Compute the distance between successive vehicle locations.
//   - Estimate vehicle speed based on elapsed time between updates.
//   - Detect anomalies in vehicle movement patterns (e.g., unrealistic jumps).

type VehicleLastSeen struct {
	Mu                 sync.RWMutex
	Store              map[string]map[string]LastSeen
	collectionInterval time.Duration
}

// NewVehicleLastSeen creates and returns a new VehicleLastSeen instance
// with an initialized storage map. This is the constructor for VehicleLastSeen.
func NewVehicleLastSeen() *VehicleLastSeen {
	return &VehicleLastSeen{
		Store:              make(map[string]map[string]LastSeen),
		collectionInterval: 30 * time.Second,
	}
}

func (vehicleLastSeen *VehicleLastSeen) SetCollectionInterval(interval time.Duration) {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()
	if interval > 0 {
		vehicleLastSeen.collectionInterval = interval
	}
}

func (vehicleLastSeen *VehicleLastSeen) CollectionInterval() time.Duration {
	vehicleLastSeen.Mu.RLock()
	defer vehicleLastSeen.Mu.RUnlock()
	if vehicleLastSeen.collectionInterval <= 0 {
		return 30 * time.Second
	}
	return vehicleLastSeen.collectionInterval
}

// Get retrieves the LastSeen data for a specific vehicle on a given server key.
// It returns the LastSeen value and a boolean indicating whether the vehicle was found.
//
// serverKey: Composite key of the deployment (oba_base_url + agency_id).
// feedID: Identity of the GTFS-RT feed the vehicle was observed in.
// vehicleID: Unique identifier of the vehicle within its feed.
func (vehicleLastSeen *VehicleLastSeen) Get(serverKey, feedID, vehicleID string) (LastSeen, bool) {
	vehicleLastSeen.Mu.RLock()
	defer vehicleLastSeen.Mu.RUnlock()

	if vehicleLastSeen.Store == nil {
		return LastSeen{}, false
	}

	if vehicles, ok := vehicleLastSeen.Store[serverKey]; ok {
		lastSeen, ok := vehicles[vehicleKey(feedID, vehicleID)]
		return lastSeen, ok
	}
	return LastSeen{}, false
}

// Set stores or updates the LastSeen data for a specific vehicle on a given server key.
//
// serverKey: Composite key of the deployment (oba_base_url + agency_id).
// feedID: Identity of the GTFS-RT feed the vehicle was observed in.
// vehicleID: Unique identifier of the vehicle within its feed.
// lastSeen: LastSeen object containing the latest observation time and related data.
func (vehicleLastSeen *VehicleLastSeen) Set(serverKey, feedID, vehicleID string, lastSeen LastSeen) {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()

	if _, ok := vehicleLastSeen.Store[serverKey]; !ok {
		vehicleLastSeen.Store[serverKey] = make(map[string]LastSeen)
	}
	vehicleLastSeen.Store[serverKey][vehicleKey(feedID, vehicleID)] = lastSeen
}

// Count returns the number of tracked vehicles for a given server key.
//
// serverKey: Composite key of the deployment (oba_base_url + agency_id).
func (v *VehicleLastSeen) Count(serverKey string) int {
	v.Mu.RLock()
	defer v.Mu.RUnlock()

	return len(v.Store[serverKey])
}

// ClearRoutine runs a background process that periodically removes vehicles
// whose LastSeen timestamps exceed the given threshold.
//
// ctx: Context for canceling the routine.
// timeInterval: Interval at which cleanup checks are performed.
// threshold: Duration after which a vehicle entry is considered stale and removed.
func (vehicleLastSeen *VehicleLastSeen) ClearRoutine(ctx context.Context, timeInterval, threshold time.Duration) {
	ticker := time.NewTicker(timeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			vehicleLastSeen.clear(threshold)
		case <-ctx.Done():
			return
		}
	}
}

// clear removes stale vehicle entries from the store that have not been
// updated within the given threshold duration.
//
// threshold: Duration after which a vehicle entry is considered stale.
func (vehicleLastSeen *VehicleLastSeen) clear(threshold time.Duration) {
	vehicleLastSeen.Mu.Lock()

	if len(vehicleLastSeen.Store) == 0 {
		vehicleLastSeen.Mu.Unlock()
		return
	}

	now := time.Now().UTC()

	var removed []LastSeen
	for agencyID, vehicles := range vehicleLastSeen.Store {

		for vehicleID, lastSeen := range vehicles {
			lastObservation := lastSeen.ObservedAt
			if lastObservation.IsZero() {
				lastObservation = lastSeen.SourceTimestamp
				if lastObservation.IsZero() {
					lastObservation = lastSeen.Time
				}
			}
			if lastObservation.Before(now) && now.Sub(lastObservation) > threshold {
				removed = append(removed, lastSeen)
				delete(vehicleLastSeen.Store[agencyID], vehicleID)
			}
		}

		if len(vehicleLastSeen.Store[agencyID]) == 0 {
			delete(vehicleLastSeen.Store, agencyID)
		}

	}
	vehicleLastSeen.Mu.Unlock()
	deleteVehicleSeries(removed)
}

// RemoveMissing retires vehicles absent from a successful FULL_DATASET
// snapshot for one feed and agency.
func (vehicleLastSeen *VehicleLastSeen) RemoveMissing(serverKey, feedID string, seen map[string]bool) {
	vehicleLastSeen.Mu.Lock()
	var removed []LastSeen
	for key, lastSeen := range vehicleLastSeen.Store[serverKey] {
		if lastSeen.FeedID == feedID && !seen[lastSeen.VehicleID] {
			removed = append(removed, lastSeen)
			delete(vehicleLastSeen.Store[serverKey], key)
		}
	}
	if len(vehicleLastSeen.Store[serverKey]) == 0 {
		delete(vehicleLastSeen.Store, serverKey)
	}
	vehicleLastSeen.Mu.Unlock()
	deleteVehicleSeries(removed)
}

// RemoveMissingForServer retires absent vehicles for a server-scoped full
// snapshot, including agencies that disappeared from the current live list.
func (vehicleLastSeen *VehicleLastSeen) RemoveMissingForServer(server models.ObaServer, feedID string, seen map[string]map[string]bool) {
	vehicleLastSeen.Mu.Lock()
	var removed []LastSeen
	for serverKey, vehicles := range vehicleLastSeen.Store {
		if !server.OwnsServerKey(serverKey) {
			continue
		}
		feedSeen := seen[serverKey+"|"+feedID]
		for key, lastSeen := range vehicles {
			if lastSeen.FeedID == feedID && !feedSeen[lastSeen.VehicleID] {
				removed = append(removed, lastSeen)
				delete(vehicles, key)
			}
		}
		if len(vehicles) == 0 {
			delete(vehicleLastSeen.Store, serverKey)
		}
	}
	vehicleLastSeen.Mu.Unlock()
	deleteVehicleSeries(removed)
}

// RemoveFeeds retires all vehicle history for feeds removed or replaced in a
// server's realtime configuration, including their Prometheus series.
func (vehicleLastSeen *VehicleLastSeen) RemoveFeeds(server models.ObaServer, feedIDs []string) {
	if len(feedIDs) == 0 {
		return
	}
	removedFeeds := make(map[string]bool, len(feedIDs))
	for _, feedID := range feedIDs {
		removedFeeds[feedID] = true
	}
	vehicleLastSeen.Mu.Lock()
	var removed []LastSeen
	for serverKey, vehicles := range vehicleLastSeen.Store {
		if !server.OwnsServerKey(serverKey) {
			continue
		}
		for key, lastSeen := range vehicles {
			if removedFeeds[lastSeen.FeedID] {
				removed = append(removed, lastSeen)
				delete(vehicles, key)
			}
		}
		if len(vehicles) == 0 {
			delete(vehicleLastSeen.Store, serverKey)
		}
	}
	vehicleLastSeen.Mu.Unlock()
	deleteVehicleSeries(removed)
}

func deleteVehicleSeries(entries []LastSeen) {
	for _, entry := range entries {
		labels := []string{entry.VehicleID, entry.AgencyID, entry.AgencyName, entry.ServerName, entry.ServerURL, entry.FeedID}
		GtfsRtVehicleSourceTimestamp.DeleteLabelValues(labels...)
		GtfsRtVehicleStateLastChangedTimestamp.DeleteLabelValues(labels...)
		VehicleSpeedGauge.DeleteLabelValues(labels...)
		VehicleSpeedDiscrepancyRatioGauge.DeleteLabelValues(labels...)
		GtfsRtVehicleSpeedLastComputedTimestamp.DeleteLabelValues(labels...)
	}
}

// Prune removes every server key the keep predicate rejects and returns the
// removed keys.
//
// This complements ClearRoutine, which expires individual vehicles by age.
// A server that leaves the configuration stops reporting entirely, so its
// vehicles would linger until the staleness threshold elapsed — and its key
// would linger forever.
func (vehicleLastSeen *VehicleLastSeen) Prune(keep func(serverKey string) bool) []string {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()

	var removed []string
	for serverKey := range vehicleLastSeen.Store {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(vehicleLastSeen.Store, serverKey)
		}
	}
	return removed
}
