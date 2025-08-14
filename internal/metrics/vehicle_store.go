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

// VehicleLastSeen stores the most recent known location and timestamp for each vehicle per server.
//
// The outer map key is the server ID (int), and the inner map key is the vehicle ID (string).
// Each entry stores a `LastSeen` struct containing the last known latitude, longitude, and timestamp.
//
// This cache is used to:
//   - Compute the distance between successive vehicle locations.
//   - Estimate vehicle speed based on elapsed time between updates.
//   - Detect anomalies in vehicle movement patterns (e.g., unrealistic jumps).

type VehicleLastSeen struct {
	Mu    sync.RWMutex
	Store map[int]map[string]LastSeen
}

// NewVehicleLastSeen creates and returns a new VehicleLastSeen instance
// with an initialized storage map. This is the constructor for VehicleLastSeen.
func NewVehicleLastSeen() *VehicleLastSeen {
	return &VehicleLastSeen{
		Store: make(map[int]map[string]LastSeen),
	}
}

// Get retrieves the LastSeen data for a specific vehicle on a given server.
// It returns the LastSeen value and a boolean indicating whether the vehicle was found.
//
// serverID: ID of the server.
// vehicleID: Unique identifier of the vehicle.
func (vehicleLastSeen *VehicleLastSeen) Get(serverID int, vehicleID string) (LastSeen, bool) {
	vehicleLastSeen.Mu.RLock()
	defer vehicleLastSeen.Mu.RUnlock()

	if vehicleLastSeen.Store == nil {
		return LastSeen{}, false
	}

	if vehicles, ok := vehicleLastSeen.Store[serverID]; ok {
		lastSeen, ok := vehicles[vehicleID]
		return lastSeen, ok
	}
	return LastSeen{}, false
}

// Set stores or updates the LastSeen data for a specific vehicle on a given server.
//
// serverID: ID of the server.
// vehicleID: Unique identifier of the vehicle.
// lastSeen: LastSeen object containing the latest observation time and related data.
func (vehicleLastSeen *VehicleLastSeen) Set(serverID int, vehicleID string, lastSeen LastSeen) {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()

	if _, ok := vehicleLastSeen.Store[serverID]; !ok {
		vehicleLastSeen.Store[serverID] = make(map[string]LastSeen)
	}
	vehicleLastSeen.Store[serverID][vehicleID] = lastSeen
}

// Count returns the number of tracked vehicles for a given server.
//
// serverID: ID of the server to count vehicles for.
func (v *VehicleLastSeen) Count(serverID int) int {
	v.Mu.RLock()
	defer v.Mu.RUnlock()

	return len(v.Store[serverID])
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

	for serverID, vehicles := range vehicleLastSeen.Store {

		for vehicleID, lastSeen := range vehicles {
			if lastSeen.Time.Before(now) && now.Sub(lastSeen.Time) > threshold {
				delete(vehicleLastSeen.Store[serverID], vehicleID)
			}
		}

		if len(vehicleLastSeen.Store[serverID]) == 0 {
			delete(vehicleLastSeen.Store, serverID)
		}

	}
}
