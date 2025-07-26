package geo

import (
	"fmt"
	"math"
	"sync"

	"github.com/golang/geo/s2"
	"github.com/jamespfennell/gtfs"
)

// BoundingBox defines the corners of a lat/lon box
type BoundingBox struct {
	MinLat float64
	MaxLat float64
	MinLon float64
	MaxLon float64
}

// Contains checks whether the given latitude and longitude are within the bounding box
func (b *BoundingBox) Contains(lat, lon float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lon >= b.MinLon && lon <= b.MaxLon
}

// ComputeBoundingBox computes the bounding box of all stops in static GTFS
func ComputeBoundingBox(stops []gtfs.Stop) (BoundingBox, error) {
	if len(stops) == 0 {
		return BoundingBox{}, fmt.Errorf("no stops to compute bounding box")
	}

	minLat := math.MaxFloat64
	maxLat := -math.MaxFloat64
	minLon := math.MaxFloat64
	maxLon := -math.MaxFloat64

	for _, stop := range stops {
		if stop.Latitude != nil && stop.Longitude != nil {
			lat := float64(*stop.Latitude)
			lon := float64(*stop.Longitude)
			if lat < minLat {
				minLat = lat
			}
			if lat > maxLat {
				maxLat = lat
			}
			if lon < minLon {
				minLon = lon
			}
			if lon > maxLon {
				maxLon = lon
			}
		}
	}

	if minLat == math.MaxFloat64 || maxLat == -math.MaxFloat64 ||
		minLon == math.MaxFloat64 || maxLon == -math.MaxFloat64 {
		return BoundingBox{}, fmt.Errorf("no valid latitude/longitude found in stops")
	}

	return BoundingBox{
		MinLat: minLat,
		MaxLat: maxLat,
		MinLon: minLon,
		MaxLon: maxLon,
	}, nil
}

// BoundingBoxStore stores bounding boxes for each server in memory with concurrency safety
type BoundingBoxStore struct {
	mu    sync.RWMutex
	store map[int]BoundingBox
}

// NewBoundingBoxStore creates and returns a new BoundingBoxStore
func NewBoundingBoxStore() *BoundingBoxStore {
	return &BoundingBoxStore{
		store: make(map[int]BoundingBox),
	}
}

// Set stores a bounding box for a specific server ID
func (s *BoundingBoxStore) Set(serverID int, bbox BoundingBox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[serverID] = bbox
}

// Get retrieves the bounding box for a specific server ID
func (s *BoundingBoxStore) Get(serverID int) (BoundingBox, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bbox, ok := s.store[serverID]
	return bbox, ok
}

// IsValidLatLon returns true if the given latitude and longitude values
// fall within the valid geographic coordinate bounds.
//
// Latitude must be between -90 and 90 degrees, and longitude must be
// between -180 and 180 degrees.
//
// Note: This function treats the coordinate (0,0) as invalid, even though it
// is a valid location in the Gulf of Guinea. This assumption is made to help
// detect uninitialized or placeholder coordinates commonly represented as (0,0).
func IsValidLatLon(lat, lon float64) bool {
	if lat == 0 && lon == 0 {
		return false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return false
	}
	return true
}

// IsInBoundingBox checks if the lat/lon is inside the server's bounding box
func (s *BoundingBoxStore) IsInBoundingBox(serverID int, lat, lon float64) bool {
	bbox, ok := s.Get(serverID)
	if !ok {
		return false
	}
	return bbox.Contains(lat, lon)
}

// earthRadiusInMeters represents the mean radius of the Earth in meters.
//
// This value (6,371,000 meters) is defined as the Earth's volumetric mean radius,
// which is commonly used for general geospatial calculations and spherical approximations.
//
// Reference: NASA Planetary Fact Sheet – Earth
// https://nssdc.gsfc.nasa.gov/planetary/factsheet/earthfact.html
const earthRadiusInMeters = 6371000


func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	p1 := s2.LatLngFromDegrees(lat1, lon1)
	p2 := s2.LatLngFromDegrees(lat2, lon2)
	return p1.Distance(p2).Radians() * earthRadiusInMeters
}
