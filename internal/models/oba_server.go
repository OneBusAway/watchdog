package models

// ObaServer represents a OneBusAway server configuration
// TODO: Some server have multiple Agencies, so we should have a list of Agencies
type ObaServer struct {
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

// NewObaServer creates a new ObaServer instance with the provided configuration
func NewObaServer(name string, id int, baseURL, apiKey, gtfsURL, tripUpdateURL, vehiclePositionURL, gtfsRtApiKey, gtfsRtApiValue string, agencyID string) *ObaServer {
	return &ObaServer{
		Name:               name,
		ID:                 id,
		ObaBaseURL:         baseURL,
		ObaApiKey:          apiKey,
		GtfsUrl:            gtfsURL,
		TripUpdateUrl:      tripUpdateURL,
		VehiclePositionUrl: vehiclePositionURL,
		GtfsRtApiKey:       gtfsRtApiKey,
		GtfsRtApiValue:     gtfsRtApiValue,
		AgencyID:           agencyID,
	}
}
