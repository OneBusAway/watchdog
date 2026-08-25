package config

import (
	"testing"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

var _ = models.ObaServer{}

func TestResolveScopeAgencyMode(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "Test Server",
		AgencyName:      "Test Agency",
		AgencyID:        "agency-1",
		ObaBaseURL:      "https://test.example.com",
		ObaApiKey:       "test-key",
		GtfsStaticFeeds: []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}

	staticStore := gtfs.NewStaticStore()
	index := gtfs.NewRouteAgencyIndex()

	scope := ResolveScope(server, staticStore, index)

	agency, ok := scope.(AgencyScope)
	if !ok {
		t.Fatalf("expected AgencyScope, got %T", scope)
	}
	if agency.AgencyID != "agency-1" {
		t.Fatalf("expected AgencyID=agency-1, got %q", agency.AgencyID)
	}
	if agency.AgencyName != "Test Agency" {
		t.Fatalf("expected AgencyName='Test Agency', got %q", agency.AgencyName)
	}
	if agency.ServerMeta.ServerName != "Test Server" {
		t.Fatalf("expected ServerName='Test Server', got %q", agency.ServerMeta.ServerName)
	}
	if agency.ServerMeta.ServerURL != "https://test.example.com" {
		t.Fatalf("expected ServerURL='https://test.example.com', got %q", agency.ServerMeta.ServerURL)
	}
}

func TestResolveScopeServerModeEmptyStore(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "Empty Server",
		ObaBaseURL:      "https://empty.example.com",
		ObaApiKey:       "test-key",
		GtfsStaticFeeds: []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}

	staticStore := gtfs.NewStaticStore()
	index := gtfs.NewRouteAgencyIndex()

	scope := ResolveScope(server, staticStore, index)

	serverScope, ok := scope.(ServerScope)
	if !ok {
		t.Fatalf("expected ServerScope, got %T", scope)
	}
	if len(serverScope.StaticAgencies) != 0 {
		t.Fatalf("expected no StaticAgencies (empty store), got %d", len(serverScope.StaticAgencies))
	}
}

func TestResolveScopeServerModeWithBundles(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "Test Server",
		ObaBaseURL:      "https://multi.example.com",
		ObaApiKey:       "test-key",
		GtfsStaticFeeds: []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}

	staticStore := gtfs.NewStaticStore()
	staticStore.Set(models.ServerKey("https://multi.example.com", "agency-A"), &models.StaticData{})
	staticStore.Set(models.ServerKey("https://multi.example.com", "agency-B"), &models.StaticData{})

	index := gtfs.NewRouteAgencyIndex()
	index.SetAgencyName("https://multi.example.com", "agency-A", "Agency Alpha")
	index.SetAgencyName("https://multi.example.com", "agency-B", "Agency Beta")

	scope := ResolveScope(server, staticStore, index)
	serverScope := scope.(ServerScope)
	if len(serverScope.StaticAgencies) != 2 {
		t.Fatalf("expected 2 StaticAgencies, got %d", len(serverScope.StaticAgencies))
	}

	names := map[string]string{}
	for _, a := range serverScope.StaticAgencies {
		names[a.AgencyID] = a.AgencyName
	}
	if names["agency-A"] != "Agency Alpha" {
		t.Errorf("expected agency-A name 'Agency Alpha', got %q", names["agency-A"])
	}
	if names["agency-B"] != "Agency Beta" {
		t.Errorf("expected agency-B name 'Agency Beta', got %q", names["agency-B"])
	}
}

func TestResolveScopeIgnoresOtherServers(t *testing.T) {
	server := models.ObaServer{
		ServerName:      "Mine",
		ObaBaseURL:      "https://mine.example.com",
		ObaApiKey:       "test-key",
		GtfsStaticFeeds: []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}

	staticStore := gtfs.NewStaticStore()
	// Bundle for another server — must not appear in Mine's scope.
	staticStore.Set(models.ServerKey("https://other.example.com", "agency-X"), &models.StaticData{})
	// Bundle for our server — must appear.
	staticStore.Set(models.ServerKey("https://mine.example.com", "agency-Mine"), &models.StaticData{})

	index := gtfs.NewRouteAgencyIndex()
	scope := ResolveScope(server, staticStore, index).(ServerScope)

	if len(scope.StaticAgencies) != 1 {
		t.Fatalf("expected 1 agency for Mine, got %d", len(scope.StaticAgencies))
	}
	if scope.StaticAgencies[0].AgencyID != "agency-Mine" {
		t.Fatalf("expected agency-Mine, got %q", scope.StaticAgencies[0].AgencyID)
	}
}
