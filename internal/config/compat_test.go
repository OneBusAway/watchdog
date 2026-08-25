package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestDecodeServerEntry(t *testing.T) {
	t.Run("legacy v1 entry is converted to the current schema", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "Test Server 1",
			"id": 1,
			"oba_base_url": "https://test1.example.com",
			"oba_api_key": "test-key-1",
			"gtfs_url": "https://gtfs1.example.com",
			"trip_update_url": "https://trip1.example.com",
			"vehicle_position_url": "https://vehicle1.example.com",
			"gtfs_rt_api_key": "api-key-1",
			"gtfs_rt_api_value": "api-value-1",
			"agency_id": "agency-1"
		}`)

		got, err := decodeServerEntry(raw)
		if err != nil {
			t.Fatalf("decodeServerEntry failed: %v", err)
		}

		expected := models.ObaServer{
			ServerName:      "Test Server 1",
			AgencyName:      "Test Server 1",
			AgencyID:        "agency-1",
			ObaBaseURL:      "https://test1.example.com",
			ObaApiKey:       "test-key-1",
			GtfsStaticFeeds: []string{"https://gtfs1.example.com"},
			GtfsRTFeeds: []models.GtfsRTFeed{{
				TripUpdateURL:      "https://trip1.example.com",
				VehiclePositionURL: "https://vehicle1.example.com",
				GtfsRTAPIKey:       "api-key-1",
				GtfsRTAPIValue:     "api-value-1",
				AgencyIDs:          []string{"agency-1"},
			}},
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("expected %+v, got %+v", expected, got)
		}
	})

	t.Run("legacy v1 entry without id is accepted", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "No ID",
			"oba_base_url": "https://test.example.com",
			"oba_api_key": "test-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-1"
		}`)

		if _, err := decodeServerEntry(raw); err != nil {
			t.Fatalf("expected legacy entry without id to decode, got: %v", err)
		}
	})

	t.Run("legacy v1 entry without agency_id becomes server-scoped", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "Server Only",
			"oba_base_url": "https://server.example.com",
			"oba_api_key": "key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com"
		}`)

		got, err := decodeServerEntry(raw)
		if err != nil {
			t.Fatalf("expected legacy entry without agency_id to decode, got: %v", err)
		}
		if got.AgencyID != "" {
			t.Errorf("expected empty AgencyID for server-scoped legacy entry, got %q", got.AgencyID)
		}
		if got.ServerName != "Server Only" {
			t.Errorf("expected ServerName='Server Only', got %q", got.ServerName)
		}
	})

	t.Run("legacy v1 name becomes server_name", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "Legacy Server",
			"oba_base_url": "https://server.example.com",
			"oba_api_key": "key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-legacy"
		}`)

		got, err := decodeServerEntry(raw)
		if err != nil {
			t.Fatalf("expected legacy entry to decode, got: %v", err)
		}
		if got.ServerName != "Legacy Server" {
			t.Errorf("expected ServerName='Legacy Server', got %q", got.ServerName)
		}
		// When agency_id is present, name also fills agency_name (mirroring the
		// previous v2 mapping).
		if got.AgencyName != "Legacy Server" {
			t.Errorf("expected AgencyName='Legacy Server', got %q", got.AgencyName)
		}
	})

	t.Run("legacy v1 entry missing a required field is rejected with v1 field name", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "No Vehicle URL",
			"oba_base_url": "https://test.example.com",
			"oba_api_key": "test-key",
			"gtfs_url": "https://gtfs.example.com",
			"agency_id": "agency-1"
		}`)

		_, err := decodeServerEntry(raw)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "vehicle_position_url") {
			t.Fatalf("expected error to name the v1 field vehicle_position_url, got: %v", err)
		}
	})

	t.Run("current v2 entry decodes unchanged", func(t *testing.T) {
		raw := json.RawMessage(`{
			"server_name": "Test Server",
			"agency_name": "Test Server",
			"oba_base_url": "https://test.example.com",
			"oba_api_key": "test-key",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"trip_update_url": "https://trip.example.com", "vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		}`)

		got, err := decodeServerEntry(raw)
		if err != nil {
			t.Fatalf("decodeServerEntry failed: %v", err)
		}

		expected := models.ObaServer{
			ServerName:      "Test Server",
			AgencyName:      "Test Server",
			AgencyID:        "agency-1",
			ObaBaseURL:      "https://test.example.com",
			ObaApiKey:       "test-key",
			GtfsStaticFeeds: []string{"https://gtfs.example.com"},
			GtfsRTFeeds: []models.GtfsRTFeed{{
				TripUpdateURL:      "https://trip.example.com",
				VehiclePositionURL: "https://vehicle.example.com",
			}},
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("expected %+v, got %+v", expected, got)
		}
	})

	t.Run("entry mixing v1 and v2 fields is rejected", func(t *testing.T) {
		raw := json.RawMessage(`{
			"name": "Mixed",
			"oba_base_url": "https://test.example.com",
			"oba_api_key": "test-key",
			"gtfs_url": "https://gtfs.example.com",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-1"
		}`)

		_, err := decodeServerEntry(raw)
		if err == nil {
			t.Fatal("expected error for mixed v1/v2 entry, got nil")
		}
		if !strings.Contains(err.Error(), "mixes legacy") {
			t.Fatalf("expected error to mention mixing schemas, got: %v", err)
		}
	})

	t.Run("current v2 entry with partial auth pair is rejected", func(t *testing.T) {
		raw := json.RawMessage(`{
			"agency_name": "Partial Auth",
			"oba_base_url": "https://test.example.com",
			"oba_api_key": "test-key",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com", "gtfs_rt_api_key": "only-key"}],
			"agency_id": "agency-1"
		}`)

		_, err := decodeServerEntry(raw)
		if err == nil {
			t.Fatal("expected error for partial auth pair, got nil")
		}
	})
}

func TestLoadConfigFromFileLegacy(t *testing.T) {
	content := `[
		{
			"name": "Legacy Server",
			"id": 7,
			"oba_base_url": "https://legacy.example.com",
			"oba_api_key": "legacy-key",
			"gtfs_url": "https://gtfs.example.com",
			"trip_update_url": "https://trip.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"gtfs_rt_api_key": "api-key",
			"gtfs_rt_api_value": "api-value",
			"agency_id": "agency-legacy"
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
		t.Fatalf("expected 1 server, got %d: %+v", len(servers), servers)
	}
	if servers[0].AgencyID != "agency-legacy" {
		t.Fatalf("expected agency-legacy, got %q", servers[0].AgencyID)
	}
	if !reflect.DeepEqual(servers[0].GtfsStaticFeeds, []string{"https://gtfs.example.com"}) {
		t.Errorf("expected gtfs_static_feeds from legacy gtfs_url, got %+v", servers[0].GtfsStaticFeeds)
	}
	if len(servers[0].GtfsRTFeeds) != 1 || servers[0].GtfsRTFeeds[0].VehiclePositionURL != "https://vehicle.example.com" {
		t.Errorf("expected converted feed, got %+v", servers[0].GtfsRTFeeds)
	}
	if !reflect.DeepEqual(servers[0].GtfsRTFeeds[0].AgencyIDs, []string{"agency-legacy"}) {
		t.Errorf("expected feed agency_ids to match agency_id, got %+v", servers[0].GtfsRTFeeds[0].AgencyIDs)
	}
}

func TestLoadConfigFromFileMixedEntries(t *testing.T) {
	content := `[
		{
			"name": "Legacy Server",
			"oba_base_url": "https://legacy.example.com",
			"oba_api_key": "legacy-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-legacy"
		},
		{
			"server_name": "Current Server",
			"agency_name": "Current Server",
			"oba_base_url": "https://current.example.com",
			"oba_api_key": "current-key",
			"gtfs_static_feeds": ["https://gtfs.example.com"],
			"gtfs_rt_feeds": [{"vehicle_position_url": "https://vehicle.example.com"}],
			"agency_id": "agency-current"
		},
		{
			"server_name": "Broken Server",
			"agency_name": "Broken Server",
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_static_feeds": null,
			"gtfs_rt_feeds": null,
			"agency_id": "agency-broken"
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

	if len(servers) != 2 {
		t.Fatalf("expected 2 valid servers, got %d: %+v", len(servers), servers)
	}
	if servers[0].AgencyID != "agency-legacy" || servers[1].AgencyID != "agency-current" {
		t.Fatalf("expected valid servers kept in order, got (%s, %s)", servers[0].AgencyID, servers[1].AgencyID)
	}
}

func TestLoadConfigFromURLLegacy(t *testing.T) {
	body := `[
		{
			"name": "Legacy Remote",
			"id": 3,
			"oba_base_url": "https://legacy.example.com",
			"oba_api_key": "legacy-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-remote"
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

	if len(servers) != 1 || servers[0].AgencyID != "agency-remote" {
		t.Fatalf("expected the legacy server to be converted, got %+v", servers)
	}
	if !reflect.DeepEqual(servers[0].GtfsStaticFeeds, []string{"https://gtfs.example.com"}) {
		t.Errorf("expected gtfs_static_feeds from legacy gtfs_url, got %+v", servers[0].GtfsStaticFeeds)
	}
}
