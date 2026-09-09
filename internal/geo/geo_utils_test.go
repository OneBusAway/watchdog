package geo

// geo_utils_test.go contains unit tests for the utility functions
// in geo_utils.go, including bounding box computation, coordinate
// validation, BoundingBoxStore operations, and haversine distance.
//
// Author: Zala Vishmayraj
//
// Run tests:
//   go test ./internal/geo/... -v (all tests)
//
// Tests are isolated so failure in any will stand out.

import (
	"testing"

	remoteGtfs "github.com/jamespfennell/gtfs"
)

func float64Ptr(f float64) *float64 { return &f }

// Helpers

func makeStop(lat, lon float64) remoteGtfs.Stop {
	return remoteGtfs.Stop{
		Latitude:  float64Ptr(lat),
		Longitude: float64Ptr(lon),
	}
}

func makeStopNoCoords() remoteGtfs.Stop {
	return remoteGtfs.Stop{}
}

// computeBoundingBox

func TestComputeBoundingBox(t *testing.T) {
	t.Run("Empty slice returns error", func(t *testing.T) {
		_, err := computeBoundingBox([]remoteGtfs.Stop{})
		if err == nil {
			t.Error("Expected error for empty slice, got nil")
		}
	})

	t.Run("All stops have nil coordinates returns error", func(t *testing.T) {
		stops := []remoteGtfs.Stop{makeStopNoCoords(), makeStopNoCoords()}
		_, err := computeBoundingBox(stops)
		if err == nil {
			t.Error("Expected error when all stops have nil coordinates, got nil")
		}
	})

	t.Run("Single stop min equals max", func(t *testing.T) {
		stops := []remoteGtfs.Stop{makeStop(47.60, -122.33)}
		bbox, err := computeBoundingBox(stops)
		if err != nil {
			t.Fatal(err)
		}
		if bbox.MinLat != bbox.MaxLat || bbox.MinLon != bbox.MaxLon {
			t.Errorf("Expected min==max for single stop, got lat[%v,%v] lon[%v,%v]",
				bbox.MinLat, bbox.MaxLat, bbox.MinLon, bbox.MaxLon)
		}
	})

	t.Run("Multiple stops correct min and max", func(t *testing.T) {
		stops := []remoteGtfs.Stop{
			makeStop(10.0, 20.0),
			makeStop(50.0, 80.0),
			makeStop(30.0, 50.0),
		}
		bbox, err := computeBoundingBox(stops)
		if err != nil {
			t.Fatal(err)
		}
		if bbox.MinLat != 10.0 || bbox.MaxLat != 50.0 {
			t.Errorf("Expected lat [10, 50], got [%v, %v]", bbox.MinLat, bbox.MaxLat)
		}
		if bbox.MinLon != 20.0 || bbox.MaxLon != 80.0 {
			t.Errorf("Expected lon [20, 80], got [%v, %v]", bbox.MinLon, bbox.MaxLon)
		}
	})

	t.Run("Mix of nil and valid coordinates only counts valid", func(t *testing.T) {
		stops := []remoteGtfs.Stop{
			makeStopNoCoords(),
			makeStop(47.60, -122.33),
			makeStopNoCoords(),
		}
		bbox, err := computeBoundingBox(stops)
		if err != nil {
			t.Fatal(err)
		}
		if bbox.MinLat != 47.60 || bbox.MaxLat != 47.60 {
			t.Errorf("Expected only valid stop coordinates, got %v", bbox)
		}
	})
}

// isValidLatLon

func TestIsValidLatLon(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		expected bool
	}{
		{"Zero coordinates", 0, 0, false},
		{"Valid coordinates", 47.60, -122.33, true},
		{"Lat too low", -91, 0.1, false},
		{"Lat too high", 91, 0.1, false},
		{"Lon too low", 0.1, -181, false},
		{"Lon too high", 0.1, 181, false},
		{"Exact lat boundary min", -90, 1, true},
		{"Exact lat boundary max", 90, 1, true},
		{"Exact lon boundary min", 1, -180, true},
		{"Exact lon boundary max", 1, 180, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidLatLon(tt.lat, tt.lon)
			if result != tt.expected {
				t.Errorf("isValidLatLon(%v, %v) = %v, expected %v",
					tt.lat, tt.lon, result, tt.expected)
			}
		})
	}
}

// BoundingBoxStore

func TestBoundingBoxStore(t *testing.T) {
	t.Run("Set and Get returns correct bbox", func(t *testing.T) {
		store := NewBoundingBoxStore()
		bbox := BoundingBox{MinLat: 10, MaxLat: 50, MinLon: 20, MaxLon: 80}
		store.Set(1, bbox)
		got, ok := store.Get(1)
		if !ok {
			t.Fatal("Expected ok=true, got false")
		}
		if got != bbox {
			t.Errorf("Expected %v, got %v", bbox, got)
		}
	})

	t.Run("Get nonexistent server returns false", func(t *testing.T) {
		store := NewBoundingBoxStore()
		_, ok := store.Get(999)
		if ok {
			t.Error("Expected ok=false for nonexistent server, got true")
		}
	})

	t.Run("IsInBoundingBox with point inside returns true", func(t *testing.T) {
		store := NewBoundingBoxStore()
		store.Set(1, BoundingBox{MinLat: 10, MaxLat: 50, MinLon: 20, MaxLon: 80})
		if !store.IsInBoundingBox(1, 30, 50) {
			t.Error("Expected point inside bounding box, got false")
		}
	})

	t.Run("IsInBoundingBox with point outside returns false", func(t *testing.T) {
		store := NewBoundingBoxStore()
		store.Set(1, BoundingBox{MinLat: 10, MaxLat: 50, MinLon: 20, MaxLon: 80})
		if store.IsInBoundingBox(1, 90, 90) {
			t.Error("Expected point outside bounding box, got true")
		}
	})

	t.Run("IsInBoundingBox with nonexistent server returns false", func(t *testing.T) {
		store := NewBoundingBoxStore()
		if store.IsInBoundingBox(999, 30, 50) {
			t.Error("Expected false for nonexistent server, got true")
		}
	})
}

// haversineDistance

func TestHaversineDistance(t *testing.T) {
	t.Run("Same point returns zero", func(t *testing.T) {
		dist := haversineDistance(47.60, -122.33, 47.60, -122.33)
		if dist != 0 {
			t.Errorf("Expected 0 for same point, got %v", dist)
		}
	})

	t.Run("Known distance Seattle to New York approximately correct", func(t *testing.T) {
		// Seattle: 47.6062, -122.3321
		// New York: 40.7128, -74.0060
		// Known great-circle distance: ~3,867 km
		dist := haversineDistance(47.6062, -122.3321, 40.7128, -74.0060)
		expectedMeters := 3867000.0
		marginMeters := 50000.0 // 50km tolerance
		if dist < expectedMeters-marginMeters || dist > expectedMeters+marginMeters {
			t.Errorf("Expected ~%v meters, got %v", expectedMeters, dist)
		}
	})
}