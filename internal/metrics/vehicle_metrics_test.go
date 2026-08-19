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
