package gtfs

import "fmt"

// This file holds the collision-detection helpers used by
// mergeStaticAndDiscoverAgencies to deduplicate stops and agencies across
// multiple GTFS feeds in a server-mode configuration. They live in their own
// file so the merge function in gtfs_bundles.go stays focused on the
// merge flow itself; readers looking for "where is the stop_id collision
// logic defined?" land here.

// stopLocation captures just enough of a stop's position to detect
// collisions beyond the stop_id alone. When two feeds declare the same
// stop_id at different lat/lon coordinates, the duplicate is reported to
// Sentry and skipped — see mergeStaticAndDiscoverAgencies.
type stopLocation struct {
	lat *float64
	lon *float64
}

// agencyIdentity captures the identity fields we use to detect agency_id
// collisions across feeds. agency_id alone isn't sufficient: two feeds can
// legitimately share an id while representing different transit authorities
// (e.g., a regional id like "MTA" used by multiple operators). Comparing
// name + url surfaces the conflict instead of silently picking the first
// occurrence.
type agencyIdentity struct {
	Name string
	Url  string
}

// sameStopLocation reports whether two (lat, lon) pairs refer to the same
// physical location. nil/nil is treated as "same" (both feeds omitted the
// location); any nil on one side is treated as "different" (one feed has
// a location and the other doesn't, which is itself suspicious).
func sameStopLocation(lat1, lon1, lat2, lon2 *float64) bool {
	if lat1 == nil && lat2 == nil && lon1 == nil && lon2 == nil {
		return true
	}
	if lat1 == nil || lat2 == nil || lon1 == nil || lon2 == nil {
		return false
	}
	return *lat1 == *lat2 && *lon1 == *lon2
}

// formatLatLon renders a *float64 for inclusion in error messages. nil
// renders as "nil" so Sentry readers can distinguish an unknown location
// from a real zero coordinate.
func formatLatLon(p *float64) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%v", *p)
}
