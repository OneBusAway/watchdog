package config

import (
	"fmt"
	"strings"

	"watchdog.onebusaway.org/internal/models"
)

// ValidateServer checks that an ObaServer has all the fields Watchdog requires
// to monitor it. A common production failure mode is a service-discovery file
// that emits JSON null for feed URLs; null unmarshals to the empty string, which
// then produces cryptic `unsupported protocol scheme ""` errors on every fetch
// cycle. Validating up front turns that into a single actionable error.
//
// The GTFS-RT auth header fields (gtfs_rt_api_key / gtfs_rt_api_value) are
// optional, but when either is set the other must be set as well. trip_update_url
// is optional and not validated here.
//
// It returns an error naming every missing field, or nil if the server is valid.
func ValidateServer(server models.ObaServer) error {
	var missing []string

	requiredStrings := []struct {
		name  string
		value string
	}{
		{"agency_name", server.AgencyName},
		{"oba_base_url", server.ObaBaseURL},
		{"oba_api_key", server.ObaApiKey},
		{"agency_id", server.AgencyID},
	}
	if len(server.GtfsStaticFeeds) == 0 {
		missing = append(missing, "gtfs_static_feeds")
	}
	for i, gtfsStaticFeed := range server.GtfsStaticFeeds {
		if strings.TrimSpace(gtfsStaticFeed) == "" {
			missing = append(missing, fmt.Sprintf("gtfs_static_feeds[%d]", i))
		}
	}
	if len(server.GtfsRTFeeds) == 0 {
		missing = append(missing, "gtfs_rt_feeds")
	}
	for i, feed := range server.GtfsRTFeeds {
		if strings.TrimSpace(feed.VehiclePositionURL) == "" {
			missing = append(missing, fmt.Sprintf("gtfs_rt_feeds[%d].vehicle_position_url", i))
		}
		if (strings.TrimSpace(feed.GtfsRTAPIKey) == "") != (strings.TrimSpace(feed.GtfsRTAPIValue) == "") {
			missing = append(missing, fmt.Sprintf("gtfs_rt_feeds[%d].gtfs_rt_api_key/value", i))
		}
	}
	for _, field := range requiredStrings {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("agency %q is missing required fields: %s", server.AgencyID, strings.Join(missing, ", "))
	}
	return nil
}
