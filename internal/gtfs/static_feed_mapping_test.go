package gtfs

import (
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestClassifyStaticFeedMappingsServerModePreservesSourceProvenance(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "server",
		ObaBaseURL:      "https://oba.example.com",
		GtfsStaticFeeds: []string{"https://feeds.example.com/multi.zip", "https://feeds.example.com/blank.zip"},
	}
	observation := classifyStaticFeedMappings(server, []downloadedStaticFeed{
		{url: server.GtfsStaticFeeds[0], bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}, {Id: "B", Name: "Agency B"}}}},
		{url: server.GtfsStaticFeeds[1], bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Unidentified"}}}},
	})

	if len(observation.Results) != 2 {
		t.Fatalf("expected one result per parsed source feed, got %+v", observation.Results)
	}
	if got := observation.Results[0].Mappings; len(got) != 2 || got[0].AgencyID != "A" || got[1].AgencyID != "B" {
		t.Fatalf("expected only the two agencies actually declared by the multi-agency feed, got %+v", got)
	}
	if got := observation.Results[1].Failures; len(got) != 1 || got[0].Reason != StaticFeedMappingMissingAgency {
		t.Fatalf("expected a sole blank agency to fail honestly in server mode, got %+v", got)
	}
}

func TestClassifyStaticFeedMappingsAgencyModeReasons(t *testing.T) {
	server := models.ObaServer{
		ServerName: "agency", ObaBaseURL: "https://oba.example.com", AgencyID: "wanted", AgencyName: "Wanted",
		GtfsStaticFeeds: []string{"missing", "ambiguous", "unknown", "implicit", "multi"},
	}
	observation := classifyStaticFeedMappings(server, []downloadedStaticFeed{
		{url: "missing", bundle: &remoteGtfs.Static{}},
		{url: "ambiguous", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Blank"}, {Id: "other"}}}},
		{url: "unknown", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "other"}}}},
		{url: "implicit", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Name: "Implicit"}}}},
		{url: "multi", bundle: &remoteGtfs.Static{Agencies: []remoteGtfs.Agency{{Id: "wanted", Name: "Parsed Name"}, {Id: "other"}}}},
	})

	wantReasons := []string{"missing_agency", "ambiguous_agency", "unknown_agency", "", ""}
	for i, want := range wantReasons {
		got := ""
		if len(observation.Results[i].Failures) > 0 {
			got = string(observation.Results[i].Failures[0].Reason)
		}
		if got != want {
			t.Errorf("result %d: expected reason %q, got %q", i, want, got)
		}
	}
	if got := observation.Results[3].Mappings; len(got) != 1 || got[0].AgencyID != "wanted" || got[0].AgencyName != "Wanted" {
		t.Fatalf("expected sole blank agency to inherit agency-mode config identity, got %+v", got)
	}
	if got := observation.Results[4].Mappings; len(got) != 1 || got[0].AgencyID != "wanted" || got[0].AgencyName != "Parsed Name" {
		t.Fatalf("expected only the configured agency from an explicit multi-agency feed, got %+v", got)
	}
}
