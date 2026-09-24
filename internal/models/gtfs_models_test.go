package models

import (
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

func TestNewStaticData(t *testing.T) {
	bundle := &remoteGtfs.Static{
		Stops:    []remoteGtfs.Stop{{Id: "1"}},
		Agencies: []remoteGtfs.Agency{{Id: "A1"}},
		Services: []remoteGtfs.Service{{Id: "S1"}},
		Routes:   []remoteGtfs.Route{{Id: "R1"}},
	}
	
	sd := NewStaticData(bundle)
	if len(sd.Stops) != 1 || sd.Stops[0].Id != "1" {
		t.Errorf("expected 1 stop with ID 1")
	}
	if len(sd.Agencies) != 1 || sd.Agencies[0].Id != "A1" {
		t.Errorf("expected 1 agency with ID A1")
	}
	if len(sd.Services) != 1 || sd.Services[0].Id != "S1" {
		t.Errorf("expected 1 service with ID S1")
	}
	if len(sd.Routes) != 1 || sd.Routes[0].Id != "R1" {
		t.Errorf("expected 1 route with ID R1")
	}
}

func TestNewRealtimeData(t *testing.T) {
	bundle := &remoteGtfs.Realtime{
		Vehicles: make([]remoteGtfs.Vehicle, 2),
	}
	
	rd := NewRealtimeData(bundle)
	if len(rd.Vehicles) != 2 {
		t.Errorf("expected 2 vehicles")
	}
	if rd.Vehicles[0].FeedID != "" {
		t.Errorf("expected empty FeedID")
	}
}
