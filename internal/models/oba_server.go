package models

import (
	"watchdog.onebusaway.org/internal/utils"
)

// ServerKey returns the unique identity of an OBA deployment: the sanitized
// base URL plus the agency ID. Agency IDs alone are only unique within a single
// OBA server, so distinct deployments can reuse values like "1" or "MTA"; the
// composite key keeps them from colliding in stores.
//
// It is expected to be called only with validated config values (oba_base_url
// is a required field), so the degenerate "" branch of SanitizeServerURL is not
// handled specially.
func ServerKey(baseURL, agencyID string) string {
	return utils.SanitizeServerURL(baseURL) + "|" + agencyID
}

// ObaServer represents a OneBusAway server configuration
type ObaServer struct {
	AgencyName      string       `json:"agency_name"`
	AgencyID        string       `json:"agency_id"`
	ObaBaseURL      string       `json:"oba_base_url"`
	ObaApiKey       string       `json:"oba_api_key"`
	GtfsStaticFeeds []string     `json:"gtfs-static-feeds"`
	GtfsRTFeeds     []GtfsRTFeed `json:"gtfs_rt_feeds"`
}

// GtfsRTFeed is one GTFS-Realtime source. Trip updates are retained for future
// consumers; current monitoring uses vehicle positions.
type GtfsRTFeed struct {
	TripUpdateURL      string   `json:"trip_update_url"`
	VehiclePositionURL string   `json:"vehicle_position_url"`
	GtfsRTAPIKey       string   `json:"gtfs_rt_api_key"`
	GtfsRTAPIValue     string   `json:"gtfs_rt_api_value"`
	AgencyIDs          []string `json:"agency_ids"`
}

// NewObaServer creates a new ObaServer instance with the provided configuration.
func NewObaServer(agencyName, agencyID, baseURL, apiKey string, gtfsStaticFeeds []string, gtfsRTFeeds []GtfsRTFeed) *ObaServer {
	return &ObaServer{AgencyName: agencyName, AgencyID: agencyID, ObaBaseURL: baseURL, ObaApiKey: apiKey, GtfsStaticFeeds: gtfsStaticFeeds, GtfsRTFeeds: gtfsRTFeeds}
}

// ServerKey returns the unique identity of this deployment (sanitized base URL
// plus agency ID). It delegates to the package-level ServerKey so the two can
// never diverge.
func (s ObaServer) ServerKey() string {
	return ServerKey(s.ObaBaseURL, s.AgencyID)
}
