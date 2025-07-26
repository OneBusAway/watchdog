package utils

import (
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/jamespfennell/gtfs"
)

// MakeMap creates and returns a map[string]string containing a single key-value pair.
func MakeMap(key, value string) map[string]string {
	return map[string]string{key: value}
}

// HashRealtimeData generates a deterministic SHA-256 hash of the GTFS-RT data.
// It includes only the minimal fields necessary to detect meaningful changes,
// reducing hash volatility while maintaining sensitivity to relevant updates.
//
// The hashing process includes:
//   - Trips: Trip ID (RouteID, ID, StartDate, etc.) and StopTimeUpdates.
//   - Vehicles: Vehicle ID (ID, Label, LicensePlate), Position, and StopID.
//   - Alerts: ID, Cause, and Effect.
//
// Slices are sorted with stable criteria to ensure deterministic output.
//
// Note: This function uses only a subset of GTFS-RT data fields when computing
// the hash. This is sufficient and intentional for testing purposes. For example,
// when verifying that data saved in RealtimeStore matches expected input in 
// tests for FetchAndStoreGTFSRTFeed. If you intend to use this hashing logic in 
// production code or for application logic, you must consider all relevant fields 
// in the GTFS-RT data structure to avoid missing meaningful differences.

func HashRealtimeData(rt *gtfs.Realtime) ([32]byte, error) {
	type minimalVehicle struct {
		ID       string
		Position *gtfs.Position
		StopID   string
	}

	type minimalTrip struct {
		ID              gtfs.TripID
		StopTimeUpdates []gtfs.StopTimeUpdate
	}

	type minimalAlert struct {
		ID     string
		Cause  gtfs.AlertCause
		Effect gtfs.AlertEffect
	}

	trips := make([]minimalTrip, len(rt.Trips))
	for i, trip := range rt.Trips {
		trips[i] = minimalTrip{
			ID:              trip.ID,
			StopTimeUpdates: trip.StopTimeUpdates,
		}
	}

	vehicles := make([]minimalVehicle, len(rt.Vehicles))
	for i, v := range rt.Vehicles {
		var idStr, stopIDStr string
		if v.ID != nil {
			idStr = v.ID.ID + "|" + v.ID.Label + "|" + v.ID.LicensePlate
		}
		if v.StopID != nil {
			stopIDStr = *v.StopID
		}
		vehicles[i] = minimalVehicle{
			ID:       idStr,
			Position: v.Position,
			StopID:   stopIDStr,
		}
	}

	alerts := make([]minimalAlert, len(rt.Alerts))
	for i, a := range rt.Alerts {
		alerts[i] = minimalAlert{
			ID:     a.ID,
			Cause:  a.Cause,
			Effect: a.Effect,
		}
	}

	sort.Slice(trips, func(i, j int) bool {
		ti := trips[i].ID
		tj := trips[j].ID
		if ti.RouteID != tj.RouteID {
			return ti.RouteID < tj.RouteID
		}
		if ti.ID != tj.ID {
			return ti.ID < tj.ID
		}
		return ti.StartDate.Before(tj.StartDate)
	})

	sort.Slice(vehicles, func(i, j int) bool {
		return vehicles[i].ID < vehicles[j].ID
	})

	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].ID < alerts[j].ID
	})

	type minimalRealtime struct {
		Trips    []minimalTrip
		Vehicles []minimalVehicle
		Alerts   []minimalAlert
	}

	simplified := minimalRealtime{
		Trips:    trips,
		Vehicles: vehicles,
		Alerts:   alerts,
	}

	b, err := json.Marshal(simplified)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}
