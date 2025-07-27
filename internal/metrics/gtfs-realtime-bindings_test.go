package metrics

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	remoteGtfs "github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

var realtimeStore *gtfs.RealtimeStore

func TestMain(m *testing.M) {
	realtimeStore = gtfs.NewRealtimeStore()

	absPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "gtfs_rt_feed_vehicles.pb"))
	if err != nil {
		fmt.Printf("Failed to get absolute path: %v\n", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		fmt.Printf("Failed to read GTFS-RT fixture: %v\n", err)
		os.Exit(1)
	}

	realtimeData, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		fmt.Printf("Failed to parse GTFS-RT data: %v\n", err)
		os.Exit(1)
	}

	realtimeStore.Set(realtimeData)

	exitCode := m.Run()
	os.Exit(exitCode)
}

func TestCountVehiclePositions(t *testing.T) {
	t.Run("Valid GTFS-RT response", func(t *testing.T) {

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: "Value of VehiclePositionUrl",
		}
		count, err := CountVehiclePositions(server, realtimeStore)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if count < 0 {
			t.Fatalf("Expected non-negative count, got %d", count)
		}
	})

}

func TestVehiclesForAgencyAPI(t *testing.T) {
	t.Run("NilResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{"data": {"list": []}}`, http.StatusOK)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
			AgencyID:   "test-agency",
		}

		count, err := VehiclesForAgencyAPI(server)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if count != 0 {
			t.Fatalf("Expected count to be 0, got %d", count)
		}
	})

	t.Run("SuccessfulResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{"data": {"list": [{"vehicleId": "1"}, {"vehicleId": "2"}]}}`, http.StatusOK)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
			AgencyID:   "test-agency",
		}

		count, err := VehiclesForAgencyAPI(server)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if count != 2 {
			t.Fatalf("Expected count to be 2, got %d", count)
		}
	})

	t.Run("ErrorResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
			AgencyID:   "test-agency",
		}

		_, err := VehiclesForAgencyAPI(server)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
	})
}

func TestCheckVehicleCountMatch(t *testing.T) {
	t.Run("Success", func(t *testing.T) {

		obaServer := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", "GTFS-Rt Server URL 1", "test-api-value", "test-api-key", "1")

		err := CheckVehicleCountMatch(testServer, realtimeStore)
		if err != nil {
			t.Fatalf("CheckVehicleCountMatch failed: %v", err)
		}

		realtimeData := realtimeStore.Get()
		if realtimeData == nil {
			t.Fatalf("Failed to parse GTFS-RT fixture data: %v", err)
		}

		t.Log("Number of vehicles in GTFS-RT feed:", len(realtimeData.Vehicles))
	})
	t.Run("OBA API Error", func(t *testing.T) {
		obaServer := setupObaServer(t, `{}`, http.StatusInternalServerError)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", "GTFS-Rt Server URL 1", "test-api-value", "test-api-key", "1")

		err := CheckVehicleCountMatch(testServer, realtimeStore)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})
}

func TestTrackInvalidVehiclesAndStoppedOutOfBounds(t *testing.T) {
	boundingBoxStore := geo.NewBoundingBoxStore()
	boundingBoxStore.Set(1, geo.BoundingBox{
		MinLat: -90, MaxLat: 90,
		MinLon: -180, MaxLon: 180,
	})

	t.Run("Success with valid vehicle positions", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: "Value of VehiclePositionUrl",
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := TrackInvalidVehiclesAndStoppedOutOfBounds(server, boundingBoxStore, realtimeStore)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("Failure due to missing bounding box", func(t *testing.T) {

		server := models.ObaServer{
			ID:                 99, // no bounding box for this ID
			VehiclePositionUrl: "Value of VehiclePositionUrl",
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := TrackInvalidVehiclesAndStoppedOutOfBounds(server, boundingBoxStore, realtimeStore)
		if err == nil {
			t.Error("Expected error due to missing bounding box, got nil")
		}
	})
}
