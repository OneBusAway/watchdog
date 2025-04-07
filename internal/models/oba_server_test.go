package models

import "testing"

func TestNewObaServer(t *testing.T) {
	name := "Test Server"
	id := 1
	baseURL := "https://test.onebusaway.org"
	apiKey := "test-key"
	gtfsURL := "https://test.gtfs.url"
	tripUpdateURL := "https://test.tripupdate.url"
	vehiclePositionURL := "https://test.vehicleposition.url"
	GtfsRtApiKey := "test-gtfs-rt-api-key"
	GtfsRtApiValue := "test-gtfs-rt-api-value"
	agencyID := "test-agency-id"

	server := NewObaServer(
		name,
		id,
		baseURL,
		apiKey,
		gtfsURL,
		tripUpdateURL,
		vehiclePositionURL,
		GtfsRtApiKey,
		GtfsRtApiValue,
		agencyID,
	)

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"Name", server.Name, name},
		{"BaseURL", server.ObaBaseURL, baseURL},
		{"ApiKey", server.ObaApiKey, apiKey},
		{"GtfsUrl", server.GtfsUrl, gtfsURL},
		{"TripUpdateUrl", server.TripUpdateUrl, tripUpdateURL},
		{"VehiclePositionUrl", server.VehiclePositionUrl, vehiclePositionURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("NewObaServer() %s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}

	if server.ID != id {
		t.Errorf("NewObaServer() ID = %v, want %v", server.ID, id)
	}
}
