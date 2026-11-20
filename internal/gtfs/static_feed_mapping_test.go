package gtfs

import (
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestClassifyStaticFeedMappingsServerMode(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "server",
		ObaBaseURL:      "https://oba.example.com",
		GtfsStaticFeeds: []string{"https://feeds.example.com/merged.zip"},
	}
	feedURL := server.GtfsStaticFeeds[0]

	observation := classifyStaticFeedMappings(server, downloadedStaticFeed{
		url:    feedURL,
		bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}, {Id: "B", Name: "Agency B"}}},
	})
	if observation.FeedURL != feedURL {
		t.Fatalf("expected the configured feed URL, got %q", observation.FeedURL)
	}
	if got := observation.Mappings; len(got) != 2 || got[0].AgencyID != "A" || got[1].AgencyID != "B" {
		t.Fatalf("expected only the two agencies declared by the merged feed, got %+v", got)
	}
	if len(observation.Failures) != 0 {
		t.Fatalf("expected no failures, got %+v", observation.Failures)
	}

	observation = classifyStaticFeedMappings(server, downloadedStaticFeed{
		url:    feedURL,
		bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Unidentified"}}},
	})
	if got := observation.Failures; len(got) != 1 || got[0].Reason != StaticFeedMappingMissingAgency {
		t.Fatalf("expected a sole blank agency to fail honestly in server mode, got %+v", got)
	}
}

func TestClassifyStaticFeedMappingsAgencyModeReasons(t *testing.T) {
	server := models.ObaServer{
		ServerName: "agency", ObaBaseURL: "https://oba.example.com", AgencyID: "wanted", AgencyName: "Wanted",
		GtfsStaticFeeds: []string{"https://feeds.example.com/merged.zip"},
	}
	tests := []struct {
		name         string
		bundle       *remoteGtfs.Static
		wantReason   string
		wantMappings []StaticFeedAgencyMapping
	}{
		{name: "missing", bundle: &remoteGtfs.Static{}, wantReason: "missing_agency"},
		{name: "ambiguous", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Blank"}, {Id: "other"}}}, wantReason: "ambiguous_agency"},
		{name: "unknown", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "other"}}}, wantReason: "unknown_agency"},
		{
			name:         "implicit",
			bundle:       &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Implicit"}}},
			wantMappings: []StaticFeedAgencyMapping{{AgencyID: "wanted", AgencyName: "Wanted"}},
		},
		{
			name:         "multi",
			bundle:       &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "wanted", Name: "Parsed Name"}, {Id: "other"}}},
			wantMappings: []StaticFeedAgencyMapping{{AgencyID: "wanted", AgencyName: "Parsed Name"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := classifyStaticFeedMappings(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], bundle: tt.bundle})
			got := ""
			if len(observation.Failures) > 0 {
				got = string(observation.Failures[0].Reason)
			}
			if got != tt.wantReason {
				t.Errorf("expected reason %q, got %q", tt.wantReason, got)
			}
			if tt.wantMappings != nil {
				if len(observation.Mappings) != len(tt.wantMappings) || observation.Mappings[0] != tt.wantMappings[0] {
					t.Fatalf("expected mappings %+v, got %+v", tt.wantMappings, observation.Mappings)
				}
			}
		})
	}
}
