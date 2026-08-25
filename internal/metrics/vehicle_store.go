package metrics

import (
	"context"
	"sync"
	"time"
)

// LastSeen stores timestamp & coordinates for speed computation
type LastSeen struct {
	Time time.Time
	Lat  float64
	Lon  float64
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
	Mu    sync.RWMutex
	Store map[string]map[string]LastSeen
}

// NewVehicleLastSeen creates and returns a new VehicleLastSeen instance
// with an initialized storage map. This is the constructor for VehicleLastSeen.
func NewVehicleLastSeen() *VehicleLastSeen {
	return &VehicleLastSeen{
		Store: make(map[string]map[string]LastSeen),
	}
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
	defer vehicleLastSeen.Mu.Unlock()

	if len(vehicleLastSeen.Store) == 0 {
		return
	}

	now := time.Now().UTC()

	for agencyID, vehicles := range vehicleLastSeen.Store {

		for vehicleID, lastSeen := range vehicles {
			if lastSeen.Time.Before(now) && now.Sub(lastSeen.Time) > threshold {
				delete(vehicleLastSeen.Store[agencyID], vehicleID)
			}
		}

		if len(vehicleLastSeen.Store[agencyID]) == 0 {
			delete(vehicleLastSeen.Store, agencyID)
		}

	}
}
