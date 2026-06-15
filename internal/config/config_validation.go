package config

import (
	"fmt"
	"strconv"
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

	if server.ID == 0 {
		missing = append(missing, "id")
	}

	requiredStrings := []struct {
		name  string
		value string
	}{
		{"name", server.Name},
		{"oba_base_url", server.ObaBaseURL},
		{"oba_api_key", server.ObaApiKey},
		{"gtfs_url", server.GtfsUrl},
		{"vehicle_position_url", server.VehiclePositionUrl},
		{"agency_id", server.AgencyID},
	}
	for _, field := range requiredStrings {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("server %q (id %d) is missing required fields: %s",
			server.Name, server.ID, strings.Join(missing, ", "))
	}
	return nil
}

// filterValidServers returns only the servers that pass ValidateServer. Each
// invalid server is reported to Sentry and dropped so that one misconfigured
// entry (e.g. null feed URLs) cannot block monitoring of the rest of the fleet.
func filterValidServers(servers []models.ObaServer) []models.ObaServer {
	valid := make([]models.ObaServer, 0, len(servers))
	for _, server := range servers {
		if err := ValidateServer(server); err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"server_id":   strconv.Itoa(server.ID),
					"server_name": server.Name,
				},
				Level: sentry.LevelError,
			})
			continue
		}
		valid = append(valid, server)
	}
	return valid
}
