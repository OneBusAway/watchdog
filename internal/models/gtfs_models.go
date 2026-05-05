package models

import (
	obaGtfs "github.com/OneBusAway/go-gtfs"
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
	Stops    []obaGtfs.Stop
	Agencies []obaGtfs.Agency
	Services []obaGtfs.Service
}

func NewStaticData(GtfsStaticBundle *obaGtfs.Static) *StaticData {
	return &StaticData{
		Stops:    append([]obaGtfs.Stop(nil), GtfsStaticBundle.Stops...),
		Agencies: append([]obaGtfs.Agency(nil), GtfsStaticBundle.Agencies...),
		Services: append([]obaGtfs.Service(nil), GtfsStaticBundle.Services...),
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
	Vehicles []obaGtfs.Vehicle
}

func NewRealtimeData(GtfsRealtimeBundle *obaGtfs.Realtime) *RealtimeData {
	return &RealtimeData{
		Vehicles: append([]obaGtfs.Vehicle(nil), GtfsRealtimeBundle.Vehicles...),
	}
}
