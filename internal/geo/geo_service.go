package geo

import (
	remoteGtfs "github.com/jamespfennell/gtfs"
)

// For now geo package only exposes helper functions to be used by other packages.
// So we don't need to define a GeoService struct.
//
// In the future, if we need to add more complex geographic logic or state,
// we can create a GeoService struct similar to GtfsService or MetricsService.
// with the required dependency
//
// Note:
// In Case adding GeoService struct in the future,
// Make sure to update the Application struct in app.go
// and the New() function to include GeoService as a dependency.
// and update the newTestApplication helper function in app/test_helpers.go

// Wrappers for utility functions

func ComputeBoundingBox(stops []remoteGtfs.Stop) (BoundingBox, error) {
	return computeBoundingBox(stops)
}

func IsValidLatLon(lat, lon float64) bool {
	return isValidLatLon(lat, lon)
}

func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	return haversineDistance(lat1, lon1, lat2, lon2)
}

func GetClusterID(stop remoteGtfs.Stop) (clusterID string, clusterType string, ok bool) {
	return getClusterID(stop)
}
