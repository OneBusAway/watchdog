package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// legacyObaServer mirrors the pre-array (v1) flat server config schema. It
// exists ONLY for backward compatibility so that configs written for earlier
// Watchdog releases keep working. New configs should use models.ObaServer.
type legacyObaServer struct {
	Name               string `json:"name"`
	ID                 int    `json:"id"`
	ObaBaseURL         string `json:"oba_base_url"`
	ObaApiKey          string `json:"oba_api_key"`
	GtfsUrl            string `json:"gtfs_url"`
	TripUpdateUrl      string `json:"trip_update_url"`
	VehiclePositionUrl string `json:"vehicle_position_url"`
	GtfsRtApiKey       string `json:"gtfs_rt_api_key"`
	GtfsRtApiValue     string `json:"gtfs_rt_api_value"`
	AgencyID           string `json:"agency_id"`
}

// validateLegacy applies the exact validation rules of the previous (v1) config
// schema to a legacy server. Unlike the current schema it does not require the
// gtfs_rt_api_key / gtfs_rt_api_value header pair to be present together; both
// are optional. The legacy id field is ignored.
func validateLegacy(server legacyObaServer) error {
	var missing []string

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
		return fmt.Errorf("legacy (v1) server %q is missing required fields: %s", server.Name, strings.Join(missing, ", "))
	}
	return nil
}

// legacyToCurrent converts a validated legacy v1 server to the current schema.
func legacyToCurrent(l legacyObaServer) models.ObaServer {
	return models.ObaServer{
		AgencyName:      l.Name,
		AgencyID:        l.AgencyID,
		ObaBaseURL:      l.ObaBaseURL,
		ObaApiKey:       l.ObaApiKey,
		GtfsStaticFeeds: []string{l.GtfsUrl},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			TripUpdateURL:      l.TripUpdateUrl,
			VehiclePositionURL: l.VehiclePositionUrl,
			GtfsRTAPIKey:       l.GtfsRtApiKey,
			GtfsRTAPIValue:     l.GtfsRtApiValue,
			AgencyIDs:          []string{l.AgencyID},
		}},
	}
}

// decodeServerEntry decodes a single raw config entry, accepting either the
// current array-based schema (v2) or the legacy flat schema (v1, converted to
// v2). An entry that mixes fields from both schemas is rejected, as is an entry
// that fails validation under its own schema.
func decodeServerEntry(raw json.RawMessage) (models.ObaServer, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return models.ObaServer{}, err
	}

	hasV1 := fields["gtfs_url"] != nil || fields["trip_update_url"] != nil || fields["vehicle_position_url"] != nil
	hasV2 := fields["gtfs-static-feeds"] != nil || fields["gtfs_rt_feeds"] != nil
	if hasV1 && hasV2 {
		return models.ObaServer{}, fmt.Errorf("server entry mixes legacy (v1) and current (v2) fields; use either the flat schema or the array schema, not both")
	}

	if hasV1 {
		var legacy legacyObaServer
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return models.ObaServer{}, err
		}
		if err := validateLegacy(legacy); err != nil {
			return models.ObaServer{}, err
		}
		return legacyToCurrent(legacy), nil
	}

	var server models.ObaServer
	if err := json.Unmarshal(raw, &server); err != nil {
		return models.ObaServer{}, err
	}
	if err := ValidateServer(server); err != nil {
		return models.ObaServer{}, err
	}
	return server, nil
}

// serverTagsFromRaw extracts the agency name and ID from a raw entry for
// error tagging. Unmarshal failures are ignored since the entry is already
// invalid.
func serverTagsFromRaw(raw json.RawMessage) map[string]string {
	var tags struct {
		LegacyName string `json:"name"`
		AgencyName string `json:"agency_name"`
		AgencyID   string `json:"agency_id"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil
	}
	m := make(map[string]string, 2)
	agencyName := tags.AgencyName
	if agencyName == "" {
		agencyName = tags.LegacyName
	}
	if agencyName != "" {
		m["agency_name"] = agencyName
	}
	if tags.AgencyID != "" {
		m["agency_id"] = tags.AgencyID
	}
	return m
}

// decodeServers decodes each raw config entry into the current schema,
// converting legacy v1 entries. Invalid entries and entries that mix schemas
// are reported to Sentry and dropped, and duplicate agency IDs are rejected, so
// one misconfigured entry cannot block monitoring of the rest of the fleet.
func decodeServers(rawEntries []json.RawMessage) []models.ObaServer {
	valid := make([]models.ObaServer, 0, len(rawEntries))
	seenAgencies := make(map[string]struct{})
	for _, raw := range rawEntries {
		server, err := decodeServerEntry(raw)
		if err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags:  serverTagsFromRaw(raw),
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
