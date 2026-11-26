package geo

import (
	"math"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

// PositionValidationReason says why a position failed validation. The values
// are stored in vehicle_position_evidence.position_validation_reason, so they
// must match that column's CHECK constraint.
type PositionValidationReason string

const (
	PositionMissing                   PositionValidationReason = "missing_position"
	PositionNonFiniteCoordinate       PositionValidationReason = "non_finite_coordinate"
	PositionPlaceholderZeroCoordinate PositionValidationReason = "placeholder_zero_coordinate"
	PositionLatitudeOutOfRange        PositionValidationReason = "latitude_out_of_range"
	PositionLongitudeOutOfRange       PositionValidationReason = "longitude_out_of_range"
)

// PositionValidationReasons lists every reason in the order the checks run.
var PositionValidationReasons = []PositionValidationReason{
	PositionMissing,
	PositionNonFiniteCoordinate,
	PositionPlaceholderZeroCoordinate,
	PositionLatitudeOutOfRange,
	PositionLongitudeOutOfRange,
}

// LatLonValidationReason returns why lat/lon is not a usable position, or ""
// when it is. The first failing check wins: non-finite, then (0,0), then
// latitude range, then longitude range.
func LatLonValidationReason(lat, lon float64) PositionValidationReason {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return PositionNonFiniteCoordinate
	}
	if lat == 0 && lon == 0 {
		return PositionPlaceholderZeroCoordinate
	}
	if lat < -90 || lat > 90 {
		return PositionLatitudeOutOfRange
	}
	if lon < -180 || lon > 180 {
		return PositionLongitudeOutOfRange
	}
	return ""
}

// VehiclePosition returns a vehicle's coordinates and why they are not usable,
// or "" when they are. A vehicle with no position or only one coordinate
// reports PositionMissing and zero coordinates.
func VehiclePosition(v remoteGtfs.Vehicle) (lat, lon float64, reason PositionValidationReason) {
	if v.Position == nil || v.Position.Latitude == nil || v.Position.Longitude == nil {
		return 0, 0, PositionMissing
	}
	lat = float64(*v.Position.Latitude)
	lon = float64(*v.Position.Longitude)
	return lat, lon, LatLonValidationReason(lat, lon)
}
