package geo

import (
	"math"
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

func ptrFloat(f float64) *float64 { return &f }

func TestBoundingBoxAccumulator(t *testing.T) {
	acc := &BoundingBoxAccumulator{}
	
	// isValidLatLon
	if !isValidLatLon(1.0, 1.0) {
		t.Fatalf("isValidLatLon failed")
	}
	if isValidLatLon(0, 0) {
		t.Fatalf("isValidLatLon(0,0) should be false")
	}
	if isValidLatLon(91, 0) || isValidLatLon(-91, 0) || isValidLatLon(0, 181) || isValidLatLon(0, -181) {
		t.Fatalf("isValidLatLon failed bounds check")
	}

	// Add invalid stops
	acc.Add(remoteGtfs.Stop{}) // nil lat/lon
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(math.NaN()), Longitude: ptrFloat(1.0)}) // NaN lat
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(1.0), Longitude: ptrFloat(math.NaN())}) // NaN lon

	// Result before initialization
	_, err := acc.Result()
	if err == nil {
		t.Fatalf("Result on uninitialized should error")
	}

	bbox := BoundingBox{MinLat: 0, MaxLat: 0, MinLon: 0, MaxLon: 0}
	if bbox.Contains(1.0, 1.0) {
		t.Fatalf("Contains should be false")
	}

	// Add valid and check Result and StopCount
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(1.0), Longitude: ptrFloat(1.0)}) // hits initialized
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(2.0), Longitude: ptrFloat(2.0)}) // hits MaxLat, MaxLon
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(0.5), Longitude: ptrFloat(2.5)}) // hits MinLat, MaxLon
	acc.Add(remoteGtfs.Stop{Latitude: ptrFloat(2.5), Longitude: ptrFloat(0.5)}) // hits MaxLat, MinLon
	
	if acc.StopCount() != 7 {
		t.Fatalf("StopCount %d != 7", acc.StopCount())
	}
	
	res, err := acc.Result()
	if err != nil {
		t.Fatalf("Result error: %v", err)
	}
	if res.MinLat != 0.5 || res.MaxLat != 2.5 || res.MinLon != 0.5 || res.MaxLon != 2.5 {
		t.Fatalf("Result bounds incorrect: %+v", res)
	}

	// Contains inside
	if !res.Contains(1.5, 1.5) {
		t.Fatalf("Contains 1.5, 1.5 should be true")
	}
	// Contains outside
	if res.Contains(3.0, 3.0) {
		t.Fatalf("Contains 3.0, 3.0 should be false")
	}

	// computeBoundingBox
	stops := []remoteGtfs.Stop{
		{Latitude: ptrFloat(1.0), Longitude: ptrFloat(1.0)},
		{Latitude: ptrFloat(3.0), Longitude: ptrFloat(3.0)},
	}
	cb, err := computeBoundingBox(stops)
	if err != nil {
		t.Fatalf("computeBoundingBox error: %v", err)
	}
	if cb.MinLat != 1.0 || cb.MaxLat != 3.0 || cb.MinLon != 1.0 || cb.MaxLon != 3.0 {
		t.Fatalf("computeBoundingBox incorrect")
	}
    
	// computeBoundingBox error
	_, err = computeBoundingBox(nil)
	if err == nil {
		t.Fatalf("expected error from computeBoundingBox")
	}
	_, err = computeBoundingBox([]remoteGtfs.Stop{{}})
	if err == nil {
		t.Fatalf("expected error from computeBoundingBox empty lat/lon")
	}

	// IsInBoundingBox
	store := NewBoundingBoxStore()
	store.Set("key1", cb)
	if !store.IsInBoundingBox("key1", 2.0, 2.0) {
		t.Fatalf("IsInBoundingBox inside failed")
	}
	if store.IsInBoundingBox("key1", 4.0, 4.0) {
		t.Fatalf("IsInBoundingBox outside failed")
	}
	if store.IsInBoundingBox("key2", 2.0, 2.0) {
		t.Fatalf("IsInBoundingBox missing key should fail")
	}
}
