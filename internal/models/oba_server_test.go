package models

import (
	"reflect"
	"testing"
)

func TestNewObaServer(t *testing.T) {
	name := "Test Server"
	agencyID := "test-agency-id"
	baseURL := "https://test.onebusaway.org"
	apiKey := "test-key"
	gtfsURLs := []string{"https://test.gtfs.url"}
	gtfsRTFeeds := []GtfsRTFeed{{
		TripUpdateURL:      "https://test.tripupdate.url",
		VehiclePositionURL: "https://test.vehicleposition.url",
		GtfsRTAPIKey:       "test-gtfs-rt-api-key",
		GtfsRTAPIValue:     "test-gtfs-rt-api-value",
		AgencyIDs:          []string{agencyID},
	}}

	server := NewObaServer(
		name,
		agencyID,
		baseURL,
		apiKey,
		gtfsURLs,
		gtfsRTFeeds,
	)

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"Name", server.Name, name},
		{"AgencyID", server.AgencyID, agencyID},
		{"BaseURL", server.ObaBaseURL, baseURL},
		{"ApiKey", server.ObaApiKey, apiKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("NewObaServer() %s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}

	if len(server.GtfsURLs) != 1 || server.GtfsURLs[0] != gtfsURLs[0] {
		t.Errorf("NewObaServer() GtfsURLs = %v, want %v", server.GtfsURLs, gtfsURLs)
	}
	if !reflect.DeepEqual(server.GtfsRTFeeds, gtfsRTFeeds) {
		t.Errorf("NewObaServer() GtfsRTFeeds = %v, want %v", server.GtfsRTFeeds, gtfsRTFeeds)
	}
}
