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
	store := testRealtimeStore(t, "agency-a")
	count, err := countVehiclePositions(models.ObaServer{AgencyID: "agency-a"}, store)
	if err != nil || count == 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := countVehiclePositions(models.ObaServer{AgencyID: "agency-b"}, store); err == nil {
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
	const agencyID = "agency-a"
	store := testRealtimeStore(t, agencyID)
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(agencyID, geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(models.ObaServer{AgencyID: agencyID}, bounds, store); err != nil {
		t.Fatal(err)
	}
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(models.ObaServer{AgencyID: "missing"}, bounds, store); err == nil {
		t.Fatal("expected missing feed error")
	}
}
