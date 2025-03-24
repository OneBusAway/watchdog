package metrics

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jamespfennell/gtfs"
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

func TestIsPositionWithinBoundary(t *testing.T) {
	boundary := &RegionBoundary{
		MinLat: 47.5,
		MaxLat: 47.7,
		MinLon: -122.4,
		MaxLon: -122.2,
	}

	testCases := []struct {
		name     string
		lat      float64
		lon      float64
		expectIn bool
	}{
		{"Inside Boundary", 47.6, -122.3, true},
		{"Outside Boundary North", 47.8, -122.3, false},
		{"Outside Boundary South", 47.4, -122.3, false},
		{"Outside Boundary East", 47.6, -122.1, false},
		{"Outside Boundary West", 47.6, -122.5, false},
		{"At Boundary Edge", 47.5, -122.4, true},
		{"Invalid Coordinates", 100.0, -122.3, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsPositionWithinBoundary(tc.lat, tc.lon, boundary)
			if result != tc.expectIn {
				t.Errorf("IsPositionWithinBoundary(%v, %v) = %v, want %v",
					tc.lat, tc.lon, result, tc.expectIn)
			}
		})
	}
}

func TestExtractRegionBoundaries(t *testing.T) {
	// Create a temporary file for this test
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test_gtfs.zip")

	// Copy test file to temporary location
	originalTestFile := "../../testdata/gtfs.zip"
	originalData, err := os.ReadFile(originalTestFile)
	if err != nil {
		t.Skipf("Skipping test: could not read test data file: %v", err)
		return
	}

	err = os.WriteFile(testFile, originalData, 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test the extraction function
	boundary, err := ExtractRegionBoundaries(testFile)
	if err != nil {
		t.Fatalf("ExtractRegionBoundaries() error = %v", err)
	}

	// Basic validation of extracted boundaries
	if boundary.MinLat >= boundary.MaxLat || boundary.MinLon >= boundary.MaxLon {
		t.Errorf("Invalid boundary: MinLat=%v, MaxLat=%v, MinLon=%v, MaxLon=%v",
			boundary.MinLat, boundary.MaxLat, boundary.MinLon, boundary.MaxLon)
	}

	// Verify boundary values are reasonable (between -90/90 for lat, -180/180 for lon)
	if boundary.MinLat < -90 || boundary.MaxLat > 90 ||
		boundary.MinLon < -180 || boundary.MaxLon > 180 {
		t.Errorf("Boundary values out of valid range: MinLat=%v, MaxLat=%v, MinLon=%v, MaxLon=%v",
			boundary.MinLat, boundary.MaxLat, boundary.MinLon, boundary.MaxLon)
	}

	// The buffer should have been applied
	t.Logf("Extracted boundary: MinLat=%v, MaxLat=%v, MinLon=%v, MaxLon=%v",
		boundary.MinLat, boundary.MaxLat, boundary.MinLon, boundary.MaxLon)
}

func TestIsValidLatLong(t *testing.T) {
	testCases := []struct {
		name        string
		lat         float64
		lon         float64
		expectValid bool
	}{
		{"Valid Coordinates", 47.6, -122.3, true},
		{"Valid Edge Case Min", -90.0, -180.0, true},
		{"Valid Edge Case Max", 90.0, 180.0, true},
		{"Invalid Latitude Too High", 91.0, 0.0, false},
		{"Invalid Latitude Too Low", -91.0, 0.0, false},
		{"Invalid Longitude Too High", 0.0, 181.0, false},
		{"Invalid Longitude Too Low", 0.0, -181.0, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isValidLatLong(tc.lat, tc.lon)
			if result != tc.expectValid {
				t.Errorf("isValidLatLong(%v, %v) = %v, want %v",
					tc.lat, tc.lon, result, tc.expectValid)
			}
		})
	}
}
