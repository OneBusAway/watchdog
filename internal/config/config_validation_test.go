package config

import (
	"context"
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
		Name:               "Test Server",
		ID:                 1,
		ObaBaseURL:         "https://test.example.com",
		ObaApiKey:          "test-key",
		GtfsUrl:            "https://gtfs.example.com",
		TripUpdateUrl:      "https://trip.example.com",
		VehiclePositionUrl: "https://vehicle.example.com",
		GtfsRtApiKey:       "",
		GtfsRtApiValue:     "",
		AgencyID:           "agency-1",
	}
}

func TestValidateServer(t *testing.T) {
	t.Run("valid server passes", func(t *testing.T) {
		if err := ValidateServer(validServer()); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	t.Run("empty optional GTFS-RT auth fields are allowed", func(t *testing.T) {
		s := validServer()
		s.GtfsRtApiKey = ""
		s.GtfsRtApiValue = ""
		s.TripUpdateUrl = ""
		if err := ValidateServer(s); err != nil {
			t.Fatalf("expected no error for empty optional fields, got: %v", err)
		}
	})

	requiredFieldCases := []struct {
		name      string
		mutate    func(*models.ObaServer)
		wantField string
	}{
		{"missing gtfs_url", func(s *models.ObaServer) { s.GtfsUrl = "" }, "gtfs_url"},
		{"missing vehicle_position_url", func(s *models.ObaServer) { s.VehiclePositionUrl = "" }, "vehicle_position_url"},
		{"missing oba_base_url", func(s *models.ObaServer) { s.ObaBaseURL = "" }, "oba_base_url"},
		{"missing oba_api_key", func(s *models.ObaServer) { s.ObaApiKey = "" }, "oba_api_key"},
		{"missing agency_id", func(s *models.ObaServer) { s.AgencyID = "" }, "agency_id"},
		{"missing name", func(s *models.ObaServer) { s.Name = "" }, "name"},
		{"missing id", func(s *models.ObaServer) { s.ID = 0 }, "id"},
		{"whitespace-only agency_id", func(s *models.ObaServer) { s.AgencyID = "   " }, "agency_id"},
		{"whitespace-only gtfs_url", func(s *models.ObaServer) { s.GtfsUrl = "  \t " }, "gtfs_url"},
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
		// Mirrors the production config where every feed field was null.
		s := models.ObaServer{
			Name:       "Intercity Transit",
			ID:         33,
			ObaBaseURL: "https://intercity-transit-oba-server.onrender.com",
			ObaApiKey:  "org.onebusaway.iphone",
		}
		err := ValidateServer(s)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		for _, field := range []string{"gtfs_url", "vehicle_position_url", "agency_id"} {
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
			"name": "Valid Server", "id": 1,
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-1"
		},
		{
			"name": "Broken Server", "id": 2,
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_url": null,
			"vehicle_position_url": null,
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
	if servers[0].ID != 1 {
		t.Fatalf("expected the valid server (id 1) to be kept, got id %d", servers[0].ID)
	}
}

// loadConfigFromURL is an independent call site from loadConfigFromFile and is
// the production path (remote config + periodic refresh), so it needs its own
// coverage that invalid servers are filtered out.
func TestLoadConfigFromURLFiltersInvalidServers(t *testing.T) {
	body := `[
		{
			"name": "Valid Server", "id": 1,
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-1"
		},
		{
			"name": "Broken Server", "id": 2,
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_url": null,
			"vehicle_position_url": null,
			"agency_id": null
		}
	]`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer ts.Close()

	servers, err := loadConfigFromURL(context.Background(), &http.Client{Timeout: 10 * time.Second}, ts.URL, "", "", 1)
	if err != nil {
		t.Fatalf("loadConfigFromURL failed: %v", err)
	}

	if len(servers) != 1 || servers[0].ID != 1 {
		t.Fatalf("expected only the valid server (id 1), got %+v", servers)
	}
}

func TestFilterValidServers(t *testing.T) {
	t.Run("drops every invalid server and preserves the order of valid ones", func(t *testing.T) {
		valid1 := validServer()
		valid1.ID = 1
		valid2 := validServer()
		valid2.ID = 2
		invalidA := validServer()
		invalidA.ID = 10
		invalidA.GtfsUrl = ""
		invalidB := validServer()
		invalidB.ID = 11
		invalidB.AgencyID = ""

		// Interleave valid and invalid: valid, invalid, valid, invalid.
		got := filterValidServers([]models.ObaServer{valid1, invalidA, valid2, invalidB})

		if len(got) != 2 {
			t.Fatalf("expected 2 valid servers, got %d: %+v", len(got), got)
		}
		if got[0].ID != 1 || got[1].ID != 2 {
			t.Fatalf("expected valid servers kept in order (1, 2), got (%d, %d)", got[0].ID, got[1].ID)
		}
	})

	t.Run("all servers invalid yields an empty slice", func(t *testing.T) {
		bad := validServer()
		bad.GtfsUrl = ""
		got := filterValidServers([]models.ObaServer{bad})
		if len(got) != 0 {
			t.Fatalf("expected 0 valid servers, got %d", len(got))
		}
	})

	t.Run("empty input yields an empty slice", func(t *testing.T) {
		got := filterValidServers(nil)
		if len(got) != 0 {
			t.Fatalf("expected 0 servers, got %d", len(got))
		}
	})
}
