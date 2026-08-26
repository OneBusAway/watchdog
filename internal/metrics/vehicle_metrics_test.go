package metrics

import (
	"net/http"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestCountVehiclePositionsUsesAgencyStore(t *testing.T) {
	serverA := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	serverB := models.ObaServer{AgencyID: "agency-b", ObaBaseURL: "https://b.example.com", AgencyName: "Agency B"}
	store := testRealtimeStore(t, serverA)
	count, err := countVehiclePositions(serverA, nil, store, nil)
	if err != nil || count == 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := countVehiclePositions(serverB, nil, store, nil); err == nil {
		t.Fatal("expected missing agency feed error")
	}
}

func TestCountVehiclePositionsKeepsDistinctServersWithSameAgencyIDDisjoint(t *testing.T) {
	// Two deployments share the same agency ID but run on different base URLs.
	// Their series must not collide: the server_url label keeps them distinct,
	// mirroring the composite ServerKey used by the stores.
	serverA := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	serverB := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://b.example.com", AgencyName: "Agency A Clone"}

	storeA := testRealtimeStore(t, serverA)
	storeB := testRealtimeStore(t, serverB)

	countA, err := countVehiclePositions(serverA, nil, storeA, nil)
	if err != nil || countA == 0 {
		t.Fatalf("server A: count=%d err=%v", countA, err)
	}
	countB, err := countVehiclePositions(serverB, nil, storeB, nil)
	if err != nil || countB == 0 {
		t.Fatalf("server B: count=%d err=%v", countB, err)
	}
	if countA != countB {
		t.Fatalf("fixture feeds should contain the same vehicle count, got %d vs %d", countA, countB)
	}

	metricA, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverA.AgencyID,
		"agency_name": serverA.AgencyName,
		"server_name": serverA.ServerName,
		"server_url":  "https://a.example.com",
	})
	if err != nil {
		t.Fatalf("failed to read series for %s: %v", serverA.ObaBaseURL, err)
	}
	metricB, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverB.AgencyID,
		"agency_name": serverB.AgencyName,
		"server_name": serverB.ServerName,
		"server_url":  "https://b.example.com",
	})
	if err != nil {
		t.Fatalf("failed to read series for %s: %v", serverB.ObaBaseURL, err)
	}

	if metricA != float64(countA) {
		t.Fatalf("expected %s series to be %d, got %v", serverA.ObaBaseURL, countA, metricA)
	}
	if metricB != float64(countB) {
		t.Fatalf("expected %s series to be %d, got %v", serverB.ObaBaseURL, countB, metricB)
	}
}

func TestCountActiveVehiclesForAgency(t *testing.T) {
	ts := setupObaServer(t, `{"data":{"list":[{"vehicleId":"1"},{"vehicleId":"2"}]}}`, http.StatusOK)
	defer ts.Close()
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: ts.URL, ObaApiKey: "key"}
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)
	count, err := countActiveVehiclesForAgency(client, server)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestTrackInvalidVehiclesUsesAgencyBounds(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	missing := models.ObaServer{AgencyID: "missing", ObaBaseURL: "https://missing.example.com", AgencyName: "Missing"}
	store := testRealtimeStore(t, server)
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, nil, bounds, store, nil); err != nil {
		t.Fatal(err)
	}
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(missing, nil, bounds, store, nil); err == nil {
		t.Fatal("expected missing feed error")
	}
}

func TestTrackVehicleTelemetrySeparatesSameIDAcrossFeeds(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}

	now := time.Now().UTC()
	vehicleA := remoteGtfs.Vehicle{
		ID:        &remoteGtfs.VehicleID{ID: "101"},
		Position:  &remoteGtfs.Position{Latitude: float32Ptr(47.60), Longitude: float32Ptr(-122.30)},
		Timestamp: &now,
	}
	vehicleB := remoteGtfs.Vehicle{
		ID:        &remoteGtfs.VehicleID{ID: "101"},
		Position:  &remoteGtfs.Position{Latitude: float32Ptr(47.65), Longitude: float32Ptr(-122.35)},
		Timestamp: &now,
	}

	data := &models.RealtimeData{Vehicles: []models.RealtimeVehicle{
		{Vehicle: vehicleA, FeedID: "0"},
		{Vehicle: vehicleB, FeedID: "1"},
	}}
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), data)

	lastSeen := NewVehicleLastSeen()
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Same vehicle ID from different feeds must not share a last-seen slot:
	// they are two distinct physical vehicles.
	lastSeen0, ok0 := lastSeen.Get(server.ServerKey(), "0", "101")
	lastSeen1, ok1 := lastSeen.Get(server.ServerKey(), "1", "101")
	if !ok0 || !ok1 {
		t.Fatalf("expected both feeds' vehicle 101 to be tracked, got feed0=%v feed1=%v", ok0, ok1)
	}
	if got := lastSeen.Count(server.ServerKey()); got != 2 {
		t.Fatalf("expected 2 tracked vehicles (same ID, distinct feeds), got %d", got)
	}
	if lastSeen0.Lat == lastSeen1.Lat {
		t.Fatalf("expected separate last-seen slots per feed, got %+v and %+v", lastSeen0, lastSeen1)
	}
}

func float32Ptr(v float32) *float32 {
	return &v
}
