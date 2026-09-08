package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"watchdog.onebusaway.org/internal/models"
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
//
// agency_id is also optional in the legacy schema under the server-scope
// redesign: a v1 entry without agency_id becomes a server-scoped entry that
// Watchdog will resolve against /api/where/metrics.json at runtime. We only
// require the fields every entry needs (URL, key, feed URLs).
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
//
// The legacy `name` field is repurposed as the server-scoped `server_name`. In
// agency-mode (when the legacy `agency_id` is present) it also fills
// `agency_name`, mirroring the previous v2 mapping; in server-mode (no
// `agency_id`) `agency_name` is left empty so the entry is unambiguously
// server-scoped. The legacy `id` (int) field is dropped — it was unused in v2
// and is not part of the server-scope redesign.
func legacyToCurrent(l legacyObaServer) models.ObaServer {
	feed := models.GtfsRTFeed{
		TripUpdateURL:      l.TripUpdateUrl,
		VehiclePositionURL: l.VehiclePositionUrl,
		GtfsRTAPIKey:       l.GtfsRtApiKey,
		GtfsRTAPIValue:     l.GtfsRtApiValue,
	}
	// Only record an agency for the feed when the legacy entry actually named
	// one; []string{""} would advertise a feed serving an agency with an empty
	// id, which is exactly what server-mode discovery must not see.
	if strings.TrimSpace(l.AgencyID) != "" {
		feed.AgencyIDs = []string{l.AgencyID}
	}

	out := models.ObaServer{
		ServerName:      l.Name,
		ObaBaseURL:      l.ObaBaseURL,
		ObaApiKey:       l.ObaApiKey,
		GtfsStaticFeeds: []string{l.GtfsUrl},
		GtfsRTFeeds:     []models.GtfsRTFeed{feed},
	}
	if strings.TrimSpace(l.AgencyID) != "" {
		out.AgencyID = l.AgencyID
		out.AgencyName = l.Name
	}
	return out
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
	hasV2 := fields["gtfs_static_feeds"] != nil || fields["gtfs_rt_feeds"] != nil
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

// decodeServers decodes, validates, and deduplicates one configuration cycle.
func decodeServers(rawEntries []json.RawMessage, logger *slog.Logger, droppedStore *DroppedServersStore) []models.ObaServer {
	return droppedStore.Reconcile(rawEntries, logger)
}
