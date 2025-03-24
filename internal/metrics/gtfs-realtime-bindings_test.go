package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

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
