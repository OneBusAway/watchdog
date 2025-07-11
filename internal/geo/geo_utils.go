package geo

import (
	"fmt"
	"math"
	"sync"

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

// IsValidLatLon checks for lat/lon validity (not nil, not 0/0, within global range)
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
