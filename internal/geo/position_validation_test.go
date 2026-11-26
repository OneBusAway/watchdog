package geo

import (
	"math"
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

func ptrFloat32(f float32) *float32 { return &f }

func TestLatLonValidationReason(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)

	tests := []struct {
		name     string
		lat, lon float64
		want     PositionValidationReason
	}{
		{"valid", 47.6, -122.3, ""},
		{"valid on the edges", 90, -180, ""},
		{"zero latitude only is valid", 0, 10, ""},
		{"zero longitude only is valid", 10, 0, ""},
		{"NaN latitude", nan, 10, PositionNonFiniteCoordinate},
		{"NaN longitude", 10, nan, PositionNonFiniteCoordinate},
		{"infinite latitude", inf, 10, PositionNonFiniteCoordinate},
		{"negative infinite longitude", 10, -inf, PositionNonFiniteCoordinate},
		{"zero zero", 0, 0, PositionPlaceholderZeroCoordinate},
		{"latitude above 90", 90.1, 10, PositionLatitudeOutOfRange},
		{"latitude below -90", -91, 10, PositionLatitudeOutOfRange},
		{"longitude above 180", 10, 180.1, PositionLongitudeOutOfRange},
		{"longitude below -180", 10, -181, PositionLongitudeOutOfRange},

		// When more than one check fails, the earlier one wins.
		{"non-finite before out of range", nan, 200, PositionNonFiniteCoordinate},
		{"latitude before longitude", 100, 200, PositionLatitudeOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LatLonValidationReason(tt.lat, tt.lon); got != tt.want {
				t.Errorf("LatLonValidationReason(%v, %v) = %q, want %q", tt.lat, tt.lon, got, tt.want)
			}
			if got, want := isValidLatLon(tt.lat, tt.lon), tt.want == ""; got != want {
				t.Errorf("isValidLatLon(%v, %v) = %v, want %v", tt.lat, tt.lon, got, want)
			}
		})
	}
}

func TestVehiclePosition(t *testing.T) {
	tests := []struct {
		name       string
		position   *remoteGtfs.Position
		want       PositionValidationReason
		wantLatLon bool
	}{
		{"no position", nil, PositionMissing, false},
		{"no coordinates", &remoteGtfs.Position{}, PositionMissing, false},
		{"latitude only", &remoteGtfs.Position{Latitude: ptrFloat32(47.6)}, PositionMissing, false},
		{"longitude only", &remoteGtfs.Position{Longitude: ptrFloat32(-122.3)}, PositionMissing, false},
		{"valid", &remoteGtfs.Position{Latitude: ptrFloat32(47.5), Longitude: ptrFloat32(-122.25)}, "", true},
		{"NaN survives the float32 conversion", &remoteGtfs.Position{Latitude: ptrFloat32(float32(math.NaN())), Longitude: ptrFloat32(1)}, PositionNonFiniteCoordinate, true},
		{"zero zero", &remoteGtfs.Position{Latitude: ptrFloat32(0), Longitude: ptrFloat32(0)}, PositionPlaceholderZeroCoordinate, true},
		{"out of range", &remoteGtfs.Position{Latitude: ptrFloat32(95), Longitude: ptrFloat32(1)}, PositionLatitudeOutOfRange, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lon, got := VehiclePosition(remoteGtfs.Vehicle{Position: tt.position})
			if got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
			if !tt.wantLatLon {
				if lat != 0 || lon != 0 {
					t.Errorf("lat, lon = %v, %v, want 0, 0", lat, lon)
				}
				return
			}
			if wantLat := float64(*tt.position.Latitude); lat != wantLat && !(math.IsNaN(lat) && math.IsNaN(wantLat)) {
				t.Errorf("lat = %v, want %v", lat, wantLat)
			}
			if wantLon := float64(*tt.position.Longitude); lon != wantLon {
				t.Errorf("lon = %v, want %v", lon, wantLon)
			}
		})
	}
}

// Every reason the code can return must be in PositionValidationReasons,
// which mirrors the CHECK constraint on position_validation_reason.
func TestPositionValidationReasonsAreListed(t *testing.T) {
	want := []PositionValidationReason{
		"missing_position",
		"non_finite_coordinate",
		"placeholder_zero_coordinate",
		"latitude_out_of_range",
		"longitude_out_of_range",
	}
	if len(PositionValidationReasons) != len(want) {
		t.Fatalf("PositionValidationReasons = %v, want %v", PositionValidationReasons, want)
	}
	for i := range want {
		if PositionValidationReasons[i] != want[i] {
			t.Errorf("PositionValidationReasons[%d] = %q, want %q", i, PositionValidationReasons[i], want[i])
		}
	}
}
