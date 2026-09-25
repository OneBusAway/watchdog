package geo

import (
	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"testing"
)

func TestGeoServiceWrappers(t *testing.T) {
	if IsValidLatLon(0, 0) {
		t.Errorf("IsValidLatLon(0, 0) should be false")
	}

	dist := HaversineDistance(1.0, 1.0, 2.0, 2.0)
	if dist <= 0 {
		t.Errorf("HaversineDistance expected positive, got %f", dist)
	}

	stops := []remoteGtfs.Stop{
		{Latitude: ptrFloat(1.0), Longitude: ptrFloat(1.0)},
		{Latitude: ptrFloat(3.0), Longitude: ptrFloat(3.0)},
	}
	cb, err := ComputeBoundingBox(stops)
	if err != nil {
		t.Errorf("ComputeBoundingBox err: %v", err)
	}
	if cb.MinLat != 1.0 || cb.MaxLat != 3.0 || cb.MinLon != 1.0 || cb.MaxLon != 3.0 {
		t.Errorf("ComputeBoundingBox incorrect bounds")
	}

	_, ok := GetClusterID(stops[0])
	if ok {
		// Just want to call it to cover
	}
}
