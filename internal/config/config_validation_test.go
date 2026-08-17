package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// validServer returns a fully-populated server that should pass validation.
func validServer() models.ObaServer {
	return models.ObaServer{
		AgencyName: "Test Server",
		ObaBaseURL: "https://test.example.com",
		ObaApiKey:  "test-key",
		AgencyID:   "agency-1",
		GtfsURLs:   []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			TripUpdateURL:      "https://trip.example.com",
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}
}

func TestValidateServer(t *testing.T) {
	t.Run("valid server passes", func(t *testing.T) {
		if err := ValidateServer(validServer()); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	t.Run("empty optional GTFS-RT fields are allowed", func(t *testing.T) {
		s := validServer()
		s.GtfsRTFeeds[0].TripUpdateURL = ""
		s.GtfsRTFeeds[0].GtfsRTAPIKey = ""
		s.GtfsRTFeeds[0].GtfsRTAPIValue = ""
		if err := ValidateServer(s); err != nil {
			t.Fatalf("expected no error for empty optional fields, got: %v", err)
		}
	})

	requiredFieldCases := []struct {
		name      string
		mutate    func(*models.ObaServer)
		wantField string
	}{
		{"missing gtfs_urls", func(s *models.ObaServer) { s.GtfsURLs = nil }, "gtfs_urls"},
		{"missing gtfs_rt_feeds", func(s *models.ObaServer) { s.GtfsRTFeeds = nil }, "gtfs_rt_feeds"},
		{"missing vehicle position URL", func(s *models.ObaServer) { s.GtfsRTFeeds[0].VehiclePositionURL = "" }, "gtfs_rt_feeds[0].vehicle_position_url"},
		{"missing oba_base_url", func(s *models.ObaServer) { s.ObaBaseURL = "" }, "oba_base_url"},
		{"missing oba_api_key", func(s *models.ObaServer) { s.ObaApiKey = "" }, "oba_api_key"},
		{"missing agency_id", func(s *models.ObaServer) { s.AgencyID = "" }, "agency_id"},
		{"missing agency_name", func(s *models.ObaServer) { s.AgencyName = "" }, "agency_name"},
		{"whitespace-only agency_id", func(s *models.ObaServer) { s.AgencyID = "   " }, "agency_id"},
		{"whitespace-only gtfs URL", func(s *models.ObaServer) { s.GtfsURLs[0] = "  \t " }, "gtfs_urls[0]"},
	}

	for _, tc := range requiredFieldCases {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer()
			tc.mutate(&s)
			err := ValidateServer(s)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("expected error to mention %q, got: %v", tc.wantField, err)
			}
		})
	}

	t.Run("reports all missing fields at once", func(t *testing.T) {
		// Mirrors a production config where every feed field is null.
		s := models.ObaServer{
			AgencyName: "Intercity Transit",
			ObaBaseURL: "https://intercity-transit-oba-server.onrender.com",
			ObaApiKey:  "org.onebusaway.iphone",
		}
		err := ValidateServer(s)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		for _, field := range []string{"gtfs_urls", "gtfs_rt_feeds", "agency_id"} {
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("expected error to mention %q, got: %v", field, err)
			}
		}
	})
}

// loadConfigFromFile must drop servers that fail validation (e.g. the
// production config where every feed URL was null) while keeping the valid
// ones, so one bad entry can't take down monitoring for the whole fleet.
func TestLoadConfigFromFileFiltersInvalidServers(t *testing.T) {
	content := `[
		{
			"agency_name": "Valid Server",
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_urls": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		},
		{
			"agency_name": "Broken Server",
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_urls": null,
			"gtfs_rt_feeds": null,
			"agency_id": null
		}
	]`

	dir := t.TempDir()
	fp := filepath.Join(dir, "config.json")
	if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	servers, err := loadConfigFromFile(fp)
	if err != nil {
		t.Fatalf("loadConfigFromFile failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("expected 1 valid server, got %d: %+v", len(servers), servers)
	}
	if servers[0].AgencyID != "agency-1" {
		t.Fatalf("expected the valid server (agency-1) to be kept, got %q", servers[0].AgencyID)
	}
}

// loadConfigFromURL is an independent call site from loadConfigFromFile and is
// the production path (remote config + periodic refresh), so it needs its own
// coverage that invalid servers are filtered out.
func TestLoadConfigFromURLFiltersInvalidServers(t *testing.T) {
	body := `[
		{
			"agency_name": "Valid Server",
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_urls": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		},
		{
			"agency_name": "Broken Server",
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_urls": null,
			"gtfs_rt_feeds": null,
			"agency_id": null
		}
	]`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer ts.Close()

	servers, err := loadConfigFromURL(context.Background(), &http.Client{Timeout: 10 * time.Second}, ts.URL, "", "", 1)
	if err != nil {
		t.Fatalf("loadConfigFromURL failed: %v", err)
	}

	if len(servers) != 1 || servers[0].AgencyID != "agency-1" {
		t.Fatalf("expected only the valid server (agency-1), got %+v", servers)
	}
}

func TestDecodeServers(t *testing.T) {
	t.Run("drops every invalid server and preserves the order of valid ones", func(t *testing.T) {
		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"agency_name": "Valid Server",
				"oba_base_url": "https://valid.example.com",
				"oba_api_key": "valid-key",
				"gtfs_urls": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
			json.RawMessage(`{
				"agency_name": "Invalid Server",
				"oba_base_url": "https://invalid.example.com",
				"oba_api_key": "invalid-key",
				"gtfs_urls": null,
				"gtfs_rt_feeds": null,
				"agency_id": "agency-10"
			}`),
			json.RawMessage(`{
				"agency_name": "Valid Server 2",
				"oba_base_url": "https://valid2.example.com",
				"oba_api_key": "valid-key-2",
				"gtfs_urls": ["https://gtfs2.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle2.example.com"}],
				"agency_id": "agency-2"
			}`),
			json.RawMessage(`{
				"agency_name": "No Agency",
				"oba_base_url": "https://noagency.example.com",
				"oba_api_key": "key",
				"gtfs_urls": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": ""
			}`),
		}

		// Interleave valid and invalid: valid, invalid, valid, invalid.
		got := decodeServers(rawEntries)

		if len(got) != 2 {
			t.Fatalf("expected 2 valid servers, got %d: %+v", len(got), got)
		}
		if got[0].AgencyID != "agency-1" || got[1].AgencyID != "agency-2" {
			t.Fatalf("expected valid servers kept in order (agency-1, agency-2), got (%s, %s)", got[0].AgencyID, got[1].AgencyID)
		}
	})

	t.Run("drops duplicate agency ids", func(t *testing.T) {
		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"agency_name": "First",
				"oba_base_url": "https://first.example.com",
				"oba_api_key": "key",
				"gtfs_urls": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
			json.RawMessage(`{
				"agency_name": "Second",
				"oba_base_url": "https://second.example.com",
				"oba_api_key": "key",
				"gtfs_urls": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
		}

		got := decodeServers(rawEntries)
		if len(got) != 1 {
			t.Fatalf("expected 1 server after dedup, got %d", len(got))
		}
		if got[0].AgencyName != "First" {
			t.Fatalf("expected the first entry to be kept, got %q", got[0].AgencyName)
		}
	})

	t.Run("all servers invalid yields an empty slice", func(t *testing.T) {
		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"agency_name": "Bad",
				"oba_base_url": "https://bad.example.com",
				"oba_api_key": "key",
				"gtfs_urls": null,
				"gtfs_rt_feeds": null,
				"agency_id": "agency-bad"
			}`),
		}
		got := decodeServers(rawEntries)
		if len(got) != 0 {
			t.Fatalf("expected 0 valid servers, got %d", len(got))
		}
	})

	t.Run("empty input yields an empty slice", func(t *testing.T) {
		got := decodeServers(nil)
		if len(got) != 0 {
			t.Fatalf("expected 0 servers, got %d", len(got))
		}
	})
}
