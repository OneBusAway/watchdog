package models

import (
	remoteGtfs "github.com/jamespfennell/gtfs"
)

// StaticData represents the static GTFS data structure.
// It contains parts we uses from GTFS Static bundels
// which are stops, agencies, and services.
//
// IMPORTANT: 
// In the future, we may need to extend this structure
// to include more fields from the GTFS Static bundle.
// Don't forget to include them here
type StaticData struct {
		Stops     []remoteGtfs.Stop
		Agencies  []remoteGtfs.Agency
		Services  []remoteGtfs.Service
}

func NewStaticData (GtfsStaticBundle *remoteGtfs.Static) *StaticData {
	return &StaticData{
		Stops:    append([]remoteGtfs.Stop(nil), GtfsStaticBundle.Stops...),
		Agencies: append([]remoteGtfs.Agency(nil), GtfsStaticBundle.Agencies...),
		Services: append([]remoteGtfs.Service(nil), GtfsStaticBundle.Services...),
	}
} 

// RealtimeData represents the realtime GTFS data structure.
// It contains parts we uses from GTFS Realtime bundels
// which are vehicles.
// IMPORTANT: 
// In the future, we may need to extend this structure
// to include more fields from the GTFS Realtime bundle.
// Don't forget to include them here
type RealtimeData struct {
		Vehicles []remoteGtfs.Vehicle
}

func NewRealtimeData(GtfsRealtimeBundle *remoteGtfs.Realtime) *RealtimeData {
	return &RealtimeData{
		Vehicles: append([]remoteGtfs.Vehicle(nil), GtfsRealtimeBundle.Vehicles...),
	}
}
