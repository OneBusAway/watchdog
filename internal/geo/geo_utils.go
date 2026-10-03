// Package geo provides utilities for geographic computations,
// including bounding box calculation, coordinate validation,
// and distance measurement using the Haversine formula.
package geo

import (
	"fmt"
	"math"
	"sync"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/golang/geo/s2"
)

// BoundingBox defines the geographic boundaries of a rectangular area.
type BoundingBox struct {
	MinLat float64 // Minimum latitude
	MaxLat float64 // Maximum latitude
	MinLon float64 // Minimum longitude
	MaxLon float64 // Maximum longitude
}

// Contains reports whether the given latitude and longitude
// are within the bounding box.
func (b *BoundingBox) Contains(lat, lon float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lon >= b.MinLon && lon <= b.MaxLon
}

// BoundingBoxAccumulator incrementally computes geographic bounds without
// retaining the stops that produced them.
type BoundingBoxAccumulator struct {
	box         BoundingBox
	stopCount   int
	initialized bool
}

// Add incorporates one stop into the running bounds. All stops increment
// stopCount, but stops with missing or NaN coordinates cannot affect the box.
// The first valid coordinate initializes all four extrema; every later valid
// coordinate updates only the minima or maxima it exceeds.
func (a *BoundingBoxAccumulator) Add(stop remoteGtfs.Stop) {
	a.stopCount++
	if stop.Latitude == nil || stop.Longitude == nil {
		return
	}
	lat, lon := *stop.Latitude, *stop.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return
	}
	if !a.initialized {
		a.box = BoundingBox{MinLat: lat, MaxLat: lat, MinLon: lon, MaxLon: lon}
		a.initialized = true
		return
	}
	if lat < a.box.MinLat {
		a.box.MinLat = lat
	}
	if lat > a.box.MaxLat {
		a.box.MaxLat = lat
	}
	if lon < a.box.MinLon {
		a.box.MinLon = lon
	}
	if lon > a.box.MaxLon {
		a.box.MaxLon = lon
	}
}

// Result finalizes an accumulator after its source stops have been processed.
// It distinguishes an accumulator that received no stops from one that received
// stops but never saw a valid latitude/longitude pair.
func (a *BoundingBoxAccumulator) Result() (BoundingBox, error) {
	if a.stopCount == 0 {
		return BoundingBox{}, fmt.Errorf("no stops to compute bounding box")
	}
	if !a.initialized {
		return BoundingBox{}, fmt.Errorf("no valid latitude/longitude found in stops")
	}
	return a.box, nil
}

// StopCount returns the total number of stops added to the accumulator.
func (a *BoundingBoxAccumulator) StopCount() int {
	return a.stopCount
}

// computeBoundingBox returns the bounding box enclosing all valid stops.
//
// It returns an error if the input slice is empty or contains no valid lat/lon pairs.
func computeBoundingBox(stops []remoteGtfs.Stop) (BoundingBox, error) {
	acc := &BoundingBoxAccumulator{}
	for _, stop := range stops {
		acc.Add(stop)
	}
	return acc.Result()
}

// BoundingBoxStore is a concurrency-safe in-memory store for
// bounding boxes indexed by server key (oba_base_url + agency_id).
type BoundingBoxStore struct {
	mu    sync.RWMutex
	store map[string]BoundingBox
}

// NewBoundingBoxStore returns a new instance of BoundingBoxStore.
func NewBoundingBoxStore() *BoundingBoxStore {
	return &BoundingBoxStore{
		store: make(map[string]BoundingBox),
	}
}

// Set stores the bounding box associated with the given server key.
func (s *BoundingBoxStore) Set(serverKey string, bbox BoundingBox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[serverKey] = bbox
}

// Get retrieves the bounding box associated with the given server key.
//
// The second return value indicates whether a bounding box was found.
func (s *BoundingBoxStore) Get(serverKey string) (BoundingBox, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bbox, ok := s.store[serverKey]
	return bbox, ok
}

// IsInBoundingBox checks whether the given lat/lon is within the
// bounding box associated with the specified server key.
func (s *BoundingBoxStore) IsInBoundingBox(serverKey string, lat, lon float64) bool {
	bbox, ok := s.Get(serverKey)
	if !ok {
		return false
	}
	return bbox.Contains(lat, lon)
}

// isValidLatLon returns true if the given latitude and longitude values
// fall within the valid geographic coordinate bounds.
//
// Latitude must be between -90 and 90 degrees, and longitude must be
// between -180 and 180 degrees.
//
// Note: This function treats the coordinate (0,0) as invalid, even though it
// is a valid location in the Gulf of Guinea. This assumption is made to help
// detect uninitialized or placeholder coordinates commonly represented as (0,0).
func isValidLatLon(lat, lon float64) bool {
	if lat == 0 && lon == 0 {
		return false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return false
	}
	return true
}

// earthRadiusInMeters represents the mean radius of the Earth in meters.
//
// This value (6,371,000 meters) is defined as the Earth's volumetric mean radius,
// which is commonly used for general geospatial calculations and spherical approximations.
//
// Reference: NASA Planetary Fact Sheet – Earth
// https://nssdc.gsfc.nasa.gov/planetary/factsheet/earthfact.html
const earthRadiusInMeters = 6371000

// haversineDistance returns the great-circle distance in meters between
// two points specified by latitude and longitude.
//
// The result is based on the Earth's mean radius.
func haversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	p1 := s2.LatLngFromDegrees(lat1, lon1)
	p2 := s2.LatLngFromDegrees(lat2, lon2)
	return p1.Distance(p2).Radians() * earthRadiusInMeters
}

// Prune removes every bounding box whose server key the keep predicate
// rejects and returns the removed keys. See gtfs.StaticStore.Prune for why
// this exists.
func (s *BoundingBoxStore) Prune(keep func(serverKey string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []string
	for serverKey := range s.store {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.store, serverKey)
		}
	}
	return removed
}
