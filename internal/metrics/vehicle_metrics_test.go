package metrics

import (
	"net/http"
	"testing"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestCountVehiclePositionsUsesAgencyStore(t *testing.T) {
	serverA := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	serverB := models.ObaServer{AgencyID: "agency-b", ObaBaseURL: "https://b.example.com", AgencyName: "Agency B"}
	store := testRealtimeStore(t, serverA)
	count, err := countVehiclePositions(serverA, store)
	if err != nil || count == 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := countVehiclePositions(serverB, store); err == nil {
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

	countA, err := countVehiclePositions(serverA, storeA)
	if err != nil || countA == 0 {
		t.Fatalf("server A: count=%d err=%v", countA, err)
	}
	countB, err := countVehiclePositions(serverB, storeB)
	if err != nil || countB == 0 {
		t.Fatalf("server B: count=%d err=%v", countB, err)
	}
	if countA != countB {
		t.Fatalf("fixture feeds should contain the same vehicle count, got %d vs %d", countA, countB)
	}

	metricA, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverA.AgencyID,
		"agency_name": serverA.AgencyName,
		"server_url":  "https://a.example.com",
	})
	if err != nil {
		t.Fatalf("failed to read series for %s: %v", serverA.ObaBaseURL, err)
	}
	metricB, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverB.AgencyID,
		"agency_name": serverB.AgencyName,
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
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, bounds, store); err != nil {
		t.Fatal(err)
	}
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(missing, bounds, store); err == nil {
		t.Fatal("expected missing feed error")
	}
}
