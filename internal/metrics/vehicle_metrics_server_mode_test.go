package metrics

import (
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// serverModeFixture builds a server-scoped entry serving two agencies, a
// route → agency index covering both, and a merged GTFS-RT feed holding one
// vehicle per agency plus one vehicle on an unknown route.
func serverModeFixture(t *testing.T, baseURL string) (models.ObaServer, []models.ObaServer, *gtfs.RealtimeStore, *gtfs.RouteAgencyIndex) {
	t.Helper()

	server := models.ObaServer{ServerName: "multi", ObaBaseURL: baseURL}
	agencies := []models.ObaServer{
		{ServerName: "multi", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"},
		{ServerName: "multi", ObaBaseURL: baseURL, AgencyID: "agency-b", AgencyName: "Agency B"},
	}

	index := gtfs.NewRouteAgencyIndex()
	index.Set(baseURL, map[string]string{"route-a": "agency-a", "route-b": "agency-b"})

	now := time.Now().UTC()
	vehicle := func(id, routeID string, lat, lon float32) models.RealtimeVehicle {
		return models.RealtimeVehicle{
			FeedID: "0",
			Vehicle: remoteGtfs.Vehicle{
				ID:        &remoteGtfs.VehicleID{ID: id},
				Trip:      &remoteGtfs.Trip{ID: remoteGtfs.TripID{RouteID: routeID}},
				Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
				Timestamp: &now,
			},
		}
	}

	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{
		vehicle("va", "route-a", 47.60, -122.30),
		vehicle("vb", "route-b", 47.61, -122.31),
		vehicle("vx", "route-unknown", 47.62, -122.32),
	}})

	return server, agencies, store, index
}

// TestTrackVehicleTelemetryCountsEachVehicleOnceInServerMode pins the core
// server-mode contract: the telemetry pass runs once per server per tick, so
// each vehicle's report counter advances exactly once even though the server
// serves several agencies.
func TestTrackVehicleTelemetryCountsEachVehicleOnceInServerMode(t *testing.T) {
	const baseURL = "https://once.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getCounterValue(VehicleReportCount, map[string]string{
		"vehicle_id":  "va",
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  baseURL,
		"feed":        "0",
	})
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected vehicle va to be counted once per tick, got %v", got)
	}
}

// TestTrackVehicleTelemetryKeysLastSeenByAttributedAgency ensures each vehicle
// lands in the last-seen slot of the agency that actually owns its route,
// rather than under every agency on the server.
func TestTrackVehicleTelemetryKeysLastSeenByAttributedAgency(t *testing.T) {
	const baseURL = "https://attributed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	for _, tc := range []struct{ agencyID, vehicleID string }{
		{"agency-a", "va"},
		{"agency-b", "vb"},
	} {
		key := models.ServerKey(baseURL, tc.agencyID)
		if got := lastSeen.Count(key); got != 1 {
			t.Fatalf("expected 1 tracked vehicle for %s, got %d", tc.agencyID, got)
		}
		if _, ok := lastSeen.Get(key, "0", tc.vehicleID); !ok {
			t.Fatalf("expected %s to be tracked under %s", tc.vehicleID, tc.agencyID)
		}
	}
}

// TestCountVehiclePositionsBucketsByAgencyInServerMode checks that the
// position gauge reports each agency's own vehicle count instead of the
// server-wide total repeated under every agency's labels.
func TestCountVehiclePositionsBucketsByAgencyInServerMode(t *testing.T) {
	const baseURL = "https://buckets.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	total, err := countVehiclePositions(server, agencies, store, index)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected the server-wide total to be 3, got %d", total)
	}

	for _, agency := range agencies {
		got, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
			"agency_id":   agency.AgencyID,
			"agency_name": agency.AgencyName,
			"server_name": agency.ServerName,
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", agency.AgencyID, err)
		}
		if got != 1 {
			t.Fatalf("expected %s to report 1 vehicle, got %v", agency.AgencyID, got)
		}
	}
}

// TestCountVehiclePositionsZeroesAgenciesWithNoVehicles guards against a
// stuck series: an agency that had vehicles last tick and none this tick must
// report 0 rather than keeping its previous value.
func TestCountVehiclePositionsZeroesAgenciesWithNoVehicles(t *testing.T) {
	const baseURL = "https://zeroed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	if _, err := countVehiclePositions(server, agencies, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Agency B's vehicle disappears from the feed.
	data := store.Get(server.ServerKey())
	data.Vehicles = data.Vehicles[:1]
	store.Set(server.ServerKey(), data)

	if _, err := countVehiclePositions(server, agencies, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	got, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   "agency-b",
		"agency_name": "Agency B",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected agency-b to fall back to 0, got %v", got)
	}
}

// TestTrackInvalidVehiclesBucketsByAgencyInServerMode checks the invalid /
// out-of-bounds gauges are attributed per agency rather than reporting the
// server-wide count under each agency's labels.
func TestTrackInvalidVehiclesBucketsByAgencyInServerMode(t *testing.T) {
	const baseURL = "https://invalid.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	// Agency A's vehicle loses its position; agency B's stays valid.
	data := store.Get(server.ServerKey())
	data.Vehicles[0].Vehicle.Position = nil
	store.Set(server.ServerKey(), data)

	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	for _, tc := range []struct {
		agencyID, agencyName string
		want                 float64
	}{
		{"agency-a", "Agency A", 1},
		{"agency-b", "Agency B", 0},
	} {
		got, err := getMetricValue(InvalidVehicleCoordinatesGauge, map[string]string{
			"agency_id":   tc.agencyID,
			"agency_name": tc.agencyName,
			"server_name": "multi",
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", tc.agencyID, err)
		}
		if got != tc.want {
			t.Fatalf("expected %s to report %v invalid vehicles, got %v", tc.agencyID, tc.want, got)
		}
	}
}

// TestTrackVehicleTelemetryAgencyModeIgnoresRouteIndex is a regression test
// for the dispatch bug: agency-mode must attribute every vehicle to the
// configured agency, even when the route → agency index is empty because the
// static bundle failed to download.
func TestTrackVehicleTelemetryAgencyModeIgnoresRouteIndex(t *testing.T) {
	const baseURL = "https://agencymode.example.com"
	server := models.ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"}

	now := time.Now().UTC()
	lat, lon := float32(47.60), float32(-122.30)
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{{
		FeedID: "0",
		Vehicle: remoteGtfs.Vehicle{
			ID:        &remoteGtfs.VehicleID{ID: "va"},
			Trip:      &remoteGtfs.Trip{ID: remoteGtfs.TripID{RouteID: "route-a"}},
			Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
			Timestamp: &now,
		},
	}}})

	lastSeen := NewVehicleLastSeen()
	// Empty index: no route is resolvable.
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	if got := lastSeen.Count(server.ServerKey()); got != 1 {
		t.Fatalf("expected agency-mode to track the vehicle regardless of the route index, got %d", got)
	}
}

// TestTrackVehicleTelemetryZeroesTrackedVehiclesOnEmptyFeed pins a sharp
// signal: when the feed reports nothing at all, the tracked gauge drops to 0
// immediately rather than coasting on last-seen entries that ClearRoutine will
// not expire for another hour.
func TestTrackVehicleTelemetryZeroesTrackedVehiclesOnEmptyFeed(t *testing.T) {
	const baseURL = "https://emptyfeed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// The feed goes empty, but the last-seen entries are still well inside
	// the staleness threshold.
	store.Set(server.ServerKey(), &models.RealtimeData{})
	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	for _, agency := range agencies {
		got, err := getMetricValue(TrackedVehiclesGauge, map[string]string{
			"agency_id":   agency.AgencyID,
			"agency_name": agency.AgencyName,
			"server_name": agency.ServerName,
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", agency.AgencyID, err)
		}
		if got != 0 {
			t.Fatalf("expected %s to report 0 tracked vehicles on an empty feed, got %v", agency.AgencyID, got)
		}
	}
}
