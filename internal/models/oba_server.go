package models

// ObaServer represents a OneBusAway server configuration
// TODO: Some server have multiple Agencies, so we should have a list of Agencies
type ObaServer struct {
	Name        string       `json:"name"`
	AgencyID    string       `json:"agency_id"`
	ObaBaseURL  string       `json:"oba_base_url"`
	ObaApiKey   string       `json:"oba_api_key"`
	GtfsURLs    []string     `json:"gtfs_urls"`
	GtfsRTFeeds []GtfsRTFeed `json:"gtfs_rt_feeds"`
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
func NewObaServer(name, agencyID, baseURL, apiKey string, gtfsURLs []string, gtfsRTFeeds []GtfsRTFeed) *ObaServer {
	return &ObaServer{Name: name, AgencyID: agencyID, ObaBaseURL: baseURL, ObaApiKey: apiKey, GtfsURLs: gtfsURLs, GtfsRTFeeds: gtfsRTFeeds}
}
