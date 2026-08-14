package config

import (
	"fmt"
	"strings"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// ValidateServer checks that an ObaServer has all the fields Watchdog requires
// to monitor it. A common production failure mode is a service-discovery file
// that emits JSON null for feed URLs; null unmarshals to the empty string, which
// then produces cryptic `unsupported protocol scheme ""` errors on every fetch
// cycle. Validating up front turns that into a single actionable error.
//
// The GTFS-RT auth header pair (gtfs_rt_api_key / gtfs_rt_api_value) and
// trip_update_url are intentionally optional and not validated here.
//
// It returns an error naming every missing field, or nil if the server is valid.
func ValidateServer(server models.ObaServer) error {
	var missing []string

	requiredStrings := []struct {
		name  string
		value string
	}{
		{"name", server.Name},
		{"oba_base_url", server.ObaBaseURL},
		{"oba_api_key", server.ObaApiKey},
		{"agency_id", server.AgencyID},
	}
	if len(server.GtfsURLs) == 0 {
		missing = append(missing, "gtfs_urls")
	}
	for i, gtfsURL := range server.GtfsURLs {
		if strings.TrimSpace(gtfsURL) == "" {
			missing = append(missing, fmt.Sprintf("gtfs_urls[%d]", i))
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

// filterValidServers returns only the servers that pass ValidateServer. Each
// invalid server is reported to Sentry and dropped so that one misconfigured
// entry (e.g. null feed URLs) cannot block monitoring of the rest of the fleet.
func filterValidServers(servers []models.ObaServer) []models.ObaServer {
	valid := make([]models.ObaServer, 0, len(servers))
	seenAgencies := make(map[string]struct{})
	for _, server := range servers {
		if err := ValidateServer(server); err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"agency_id":   server.AgencyID,
					"server_name": server.Name,
				},
				Level: sentry.LevelError,
			})
			continue
		}
		if _, exists := seenAgencies[server.AgencyID]; exists {
			report.ReportError(fmt.Errorf("duplicate agency_id %q", server.AgencyID))
			continue
		}
		seenAgencies[server.AgencyID] = struct{}{}
		valid = append(valid, server)
	}
	return valid
}
