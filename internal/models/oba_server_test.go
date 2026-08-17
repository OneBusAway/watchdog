package models

import (
	"reflect"
	"testing"
)

func TestNewObaServer(t *testing.T) {
	agencyName := "Test Server"
	agencyID := "test-agency-id"
	baseURL := "https://test.onebusaway.org"
	apiKey := "test-key"
	gtfsStaticFeeds := []string{"https://test.gtfs.url"}
	gtfsRTFeeds := []GtfsRTFeed{{
		TripUpdateURL:      "https://test.tripupdate.url",
		VehiclePositionURL: "https://test.vehicleposition.url",
		GtfsRTAPIKey:       "test-gtfs-rt-api-key",
		GtfsRTAPIValue:     "test-gtfs-rt-api-value",
		AgencyIDs:          []string{agencyID},
	}}

	server := NewObaServer(
		agencyName,
		agencyID,
		baseURL,
		apiKey,
		gtfsStaticFeeds,
		gtfsRTFeeds,
	)

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"AgencyName", server.AgencyName, agencyName},
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

	if len(server.GtfsStaticFeeds) != 1 || server.GtfsStaticFeeds[0] != gtfsStaticFeeds[0] {
		t.Errorf("NewObaServer() GtfsStaticFeeds = %v, want %v", server.GtfsStaticFeeds, gtfsStaticFeeds)
	}
	if !reflect.DeepEqual(server.GtfsRTFeeds, gtfsRTFeeds) {
		t.Errorf("NewObaServer() GtfsRTFeeds = %v, want %v", server.GtfsRTFeeds, gtfsRTFeeds)
	}
}
