package metrics

import (
	"net/http"
	"testing"

	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestCountVehiclePositions(t *testing.T) {
	t.Run("Valid GTFS-RT response", func(t *testing.T) {
		mockServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")
		defer mockServer.Close()

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: mockServer.URL,
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		count, err := CountVehiclePositions(server)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if count < 0 {
			t.Fatalf("Expected non-negative count, got %d", count)
		}
	})

	t.Run("Unreachable server", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 3,
			VehiclePositionUrl: "http://nonexistent.local/gtfs-rt",
		}

		_, err := CountVehiclePositions(server)
		if err == nil {
			t.Fatal("Expected an error, got nil")
		}
	})

	t.Run("Invalid URL", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 4,
			VehiclePositionUrl: "://invalid-url",
		}

		_, err := CountVehiclePositions(server)
		if err == nil {
			t.Fatal("Expected an error due to invalid URL, got nil")
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
		gtfsRtServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")

		defer gtfsRtServer.Close()

		obaServer := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := CheckVehicleCountMatch(testServer)
		if err != nil {
			t.Fatalf("CheckVehicleCountMatch failed: %v", err)
		}

		realtimeData, err := gtfs.ParseRealtime(readFixture(t, "gtfs_rt_feed_vehicles.pb"), &gtfs.ParseRealtimeOptions{})
		if err != nil {
			t.Fatalf("Failed to parse GTFS-RT fixture data: %v", err)
		}

		t.Log("Number of vehicles in GTFS-RT feed:", len(realtimeData.Vehicles))
	})

	t.Run("GTFS-RT Error", func(t *testing.T) {
		// Set up a GTFS-RT server that returns an error
		gtfsRtServer := setupTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer gtfsRtServer.Close()

		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := CheckVehicleCountMatch(testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	t.Run("OBA API Error", func(t *testing.T) {
		gtfsRtServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")
		defer gtfsRtServer.Close()

		obaServer := setupObaServer(t, `{}`, http.StatusInternalServerError)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := CheckVehicleCountMatch(testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})
}

func TestTrackInvalidVehiclesAndStoppedOutOfBounds(t *testing.T) {
	store := geo.NewBoundingBoxStore()
	store.Set(1, geo.BoundingBox{
		MinLat: -90, MaxLat: 90,
		MinLon: -180, MaxLon: 180,
	})

	t.Run("Success with valid vehicle positions", func(t *testing.T) {
		mockServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")
		defer mockServer.Close()

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: mockServer.URL,
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := TrackInvalidVehiclesAndStoppedOutOfBounds(server, store)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("Failure with unreachable vehicle position server", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 2,
			VehiclePositionUrl: "http://invalid.localhost/test",
		}

		err := TrackInvalidVehiclesAndStoppedOutOfBounds(server, store)
		if err == nil {
			t.Error("Expected error due to unreachable server, got nil")
		}
	})

	t.Run("Failure due to missing bounding box", func(t *testing.T) {
		mockServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")
		defer mockServer.Close()

		server := models.ObaServer{
			ID:                 99, // no bounding box for this ID
			VehiclePositionUrl: mockServer.URL,
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := TrackInvalidVehiclesAndStoppedOutOfBounds(server, store)
		if err == nil {
			t.Error("Expected error due to missing bounding box, got nil")
		}
	})
}
