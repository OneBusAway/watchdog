package geo

import (
    "testing"

    remoteGtfs "github.com/jamespfennell/gtfs"
)

func float64Ptr(f float64) *float64 { return &f }

func TestGetClusterID_ZeroCoordinates(t *testing.T) {
    t.Run("Stop with (0,0) coordinates should not S2 cluster", func(t *testing.T) {
        stop := remoteGtfs.Stop{
            Id:        "zero-coord-stop",
            Type:      0,
            Latitude:  float64Ptr(0.0),
            Longitude: float64Ptr(0.0),
        }
        _, _, ok := getClusterID(stop)
        if ok {
            t.Error("Expected ok=false for (0,0) coordinates, got true")
        }
    })
}