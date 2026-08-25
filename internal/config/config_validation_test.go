package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
		ServerName:      "Test Server",
		AgencyName:      "Test Agency",
		ObaBaseURL:      "https://test.example.com",
		ObaApiKey:       "test-key",
		AgencyID:        "agency-1",
		GtfsStaticFeeds: []string{"https://gtfs.example.com"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			TripUpdateURL:      "https://trip.example.com",
			VehiclePositionURL: "https://vehicle.example.com",
		}},
	}
}

func TestValidateServerServerScope(t *testing.T) {
	// A server entry with no agency_id is valid: it's a server-scoped entry.
	t.Run("server-scoped entry without agency_id is valid", func(t *testing.T) {
		s := validServer()
		s.AgencyID = ""
		s.AgencyName = ""
		if err := ValidateServer(s); err != nil {
			t.Fatalf("expected server-scoped entry to validate, got: %v", err)
		}
	})

	// Agency_id paired with empty agency_name is rejected (operators must
	// provide both or neither — agency_name without agency_id is meaningless).
	t.Run("agency_id requires agency_name", func(t *testing.T) {
		s := validServer()
		s.AgencyName = ""
		err := ValidateServer(s)
		if err == nil {
			t.Fatal("expected error when agency_id is set but agency_name is empty")
		}
		if !strings.Contains(err.Error(), "agency_name") {
			t.Fatalf("expected error to mention agency_name, got: %v", err)
		}
	})
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
		{"missing gtfs_static_feeds", func(s *models.ObaServer) { s.GtfsStaticFeeds = nil }, "gtfs_static_feeds"},
		{"missing gtfs_rt_feeds", func(s *models.ObaServer) { s.GtfsRTFeeds = nil }, "gtfs_rt_feeds"},
		{"missing vehicle position URL", func(s *models.ObaServer) { s.GtfsRTFeeds[0].VehiclePositionURL = "" }, "gtfs_rt_feeds[0].vehicle_position_url"},
		{"missing oba_base_url", func(s *models.ObaServer) { s.ObaBaseURL = "" }, "oba_base_url"},
		{"missing oba_api_key", func(s *models.ObaServer) { s.ObaApiKey = "" }, "oba_api_key"},
		{"missing server_name", func(s *models.ObaServer) { s.ServerName = "" }, "server_name"},
		{"whitespace-only server_name", func(s *models.ObaServer) { s.ServerName = "  " }, "server_name"},
		{"agency_id with empty agency_name", func(s *models.ObaServer) { s.AgencyName = "" }, "agency_name"},
		{"whitespace-only gtfs URL", func(s *models.ObaServer) { s.GtfsStaticFeeds[0] = "  \t " }, "gtfs_static_feeds[0]"},
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
		for _, field := range []string{"gtfs_static_feeds", "gtfs_rt_feeds", "server_name"} {
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
			"server_name": "Test Server", "agency_name": "Valid Server",
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		},
		{
			"server_name": "Test Server", "agency_name": "Broken Server",
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_static_feeds": null,
			"gtfs_rt_feeds": null,
			"agency_id": null
		}
	]`

	dir := t.TempDir()
	fp := filepath.Join(dir, "config.json")
	if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	servers, err := loadConfigFromFile(fp, testLogger())
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
			"server_name": "Test Server", "agency_name": "Valid Server",
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		},
		{
			"server_name": "Test Server", "agency_name": "Broken Server",
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_static_feeds": null,
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

	servers, err := loadConfigFromURL(context.Background(), &http.Client{Timeout: 10 * time.Second}, ts.URL, "", "", 1, testLogger())
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
				"server_name": "Test Server", "agency_name": "Valid Server",
				"oba_base_url": "https://valid.example.com",
				"oba_api_key": "valid-key",
				"gtfs_static_feeds": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
			json.RawMessage(`{
				"server_name": "Invalid Server", "agency_name": "Invalid Server",
				"oba_base_url": "https://invalid.example.com",
				"oba_api_key": "invalid-key",
				"gtfs_static_feeds": null,
				"gtfs_rt_feeds": null,
				"agency_id": "agency-10"
			}`),
			json.RawMessage(`{
				"server_name": "Test Server 2", "agency_name": "Valid Server 2",
				"oba_base_url": "https://valid2.example.com",
				"oba_api_key": "valid-key-2",
				"gtfs_static_feeds": ["https://gtfs2.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle2.example.com"}],
				"agency_id": "agency-2"
			}`),
			json.RawMessage(`{
				"server_name": "Test Server",
				"oba_base_url": "https://noagency.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": null,
				"gtfs_rt_feeds": null
			}`),
		}

		// Interleave valid and invalid: valid, invalid, valid, invalid.
		got := decodeServers(rawEntries, testLogger())

		if len(got) != 2 {
			t.Fatalf("expected 2 valid servers, got %d: %+v", len(got), got)
		}
		if got[0].AgencyID != "agency-1" || got[1].AgencyID != "agency-2" {
			t.Fatalf("expected valid servers kept in order (agency-1, agency-2), got (%s, %s)", got[0].AgencyID, got[1].AgencyID)
		}
	})

	t.Run("drops duplicate servers with identical oba_base_url and agency_id", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"server_name": "Test Server 1", "agency_name": "First",
				"oba_base_url": "https://first.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
			json.RawMessage(`{
				"server_name": "Test Server 2", "agency_name": "Second",
				"oba_base_url": "https://first.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
		}

		got := decodeServers(rawEntries, logger)
		if len(got) != 1 {
			t.Fatalf("expected 1 server after dedup, got %d", len(got))
		}
		if got[0].AgencyName != "First" {
			t.Fatalf("expected the first entry to be kept, got %q", got[0].AgencyName)
		}
		if !strings.Contains(logBuf.String(), "Dropping server with duplicate oba_base_url and agency_id") {
			t.Errorf("expected the duplicate drop to be logged, got logs:\n%s", logBuf.String())
		}
	})

	t.Run("keeps servers that share an agency_id across different base URLs", func(t *testing.T) {
		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"server_name": "Test Server 1", "agency_name": "First",
				"oba_base_url": "https://first.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
			json.RawMessage(`{
				"server_name": "Test Server 2", "agency_name": "Second",
				"oba_base_url": "https://second.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": ["https://gtfs.example.com"],
				"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
				"agency_id": "agency-1"
			}`),
		}

		got := decodeServers(rawEntries, testLogger())
		if len(got) != 2 {
			t.Fatalf("expected both servers kept (distinct base URLs, shared agency_id), got %d: %+v", len(got), got)
		}
	})

	t.Run("all servers invalid yields an empty slice", func(t *testing.T) {
		rawEntries := []json.RawMessage{
			json.RawMessage(`{
				"server_name": "Test Server", "agency_name": "Bad",
				"oba_base_url": "https://bad.example.com",
				"oba_api_key": "key",
				"gtfs_static_feeds": null,
				"gtfs_rt_feeds": null,
				"agency_id": "agency-bad"
			}`),
		}
		got := decodeServers(rawEntries, testLogger())
		if len(got) != 0 {
			t.Fatalf("expected 0 valid servers, got %d", len(got))
		}
	})

	t.Run("empty input yields an empty slice", func(t *testing.T) {
		got := decodeServers(nil, testLogger())
		if len(got) != 0 {
			t.Fatalf("expected 0 servers, got %d", len(got))
		}
	})
}
