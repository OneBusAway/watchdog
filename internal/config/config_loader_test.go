package config

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestLoadConfigFromFile(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		content := `[{
		"name": "Test Server",
		"oba_base_url": "https://test.example.com",
		"oba_api_key": "test-key",
		"gtfs_urls": ["https://gtfs.example.com"],
		"gtfs_rt_feeds": [{"trip_update_url": "https://trip.example.com", "vehicle_position_url": "https://vehicle.example.com"}],
		"agency_id": "agency-1"
		}]`

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
			t.Fatalf("expected 1 server, got %d", len(servers))
		}

		expected := models.ObaServer{
			Name:       "Test Server",
			ObaBaseURL: "https://test.example.com",
			ObaApiKey:  "test-key",
			AgencyID:   "agency-1",
			GtfsURLs:   []string{"https://gtfs.example.com"},
			GtfsRTFeeds: []models.GtfsRTFeed{{
				TripUpdateURL:      "https://trip.example.com",
				VehiclePositionURL: "https://vehicle.example.com",
			}},
		}

		if !reflect.DeepEqual(servers[0], expected) {
			t.Errorf("expected %+v, got %+v", expected, servers[0])
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "config.json")
		content := `{ this is not valid JSON }`
		if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
			t.Fatalf("write invalid config.json: %v", err)
		}

		if _, err := loadConfigFromFile(fp); err == nil {
			t.Errorf("expected error with invalid JSON, got none")
		}
	})

	t.Run("NonExistentFile", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "config.json")

		if _, err := loadConfigFromFile(fp); err == nil {
			t.Errorf("expected error for non-existent file, got none")
		}
	})
}

func TestLoadConfigFromURL(t *testing.T) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	ctx := context.Background()
	t.Run("ValidResponse", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name": "Test Server",
			 "oba_base_url": "https://test.example.com",
			 "oba_api_key": "test-key",
			 "gtfs_urls": ["https://gtfs.example.com"],
			 "gtfs_rt_feeds": [{"trip_update_url": "https://trip.example.com", "vehicle_position_url": "https://vehicle.example.com"}],
			 "agency_id": "agency-1"
			}]`))
		}))
		defer ts.Close()

		servers, err := loadConfigFromURL(ctx, client, ts.URL, "user", "pass", 1)
		if err != nil {
			t.Fatalf("loadConfigFromURL failed: %v", err)
		}

		if len(servers) != 1 {
			t.Fatalf("Expected 1 server, got %d", len(servers))
		}

		expected := models.ObaServer{
			Name:       "Test Server",
			ObaBaseURL: "https://test.example.com",
			ObaApiKey:  "test-key",
			AgencyID:   "agency-1",
			GtfsURLs:   []string{"https://gtfs.example.com"},
			GtfsRTFeeds: []models.GtfsRTFeed{{
				TripUpdateURL:      "https://trip.example.com",
				VehiclePositionURL: "https://vehicle.example.com",
			}},
		}

		if !reflect.DeepEqual(servers[0], expected) {
			t.Errorf("Expected server %+v, got %+v", expected, servers[0])
		}
	})

	t.Run("ErrorResponse", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		_, err := loadConfigFromURL(ctx, client, ts.URL, "", "", 1)
		if err == nil {
			t.Errorf("Expected error with 500 response, got none")
		}
	})

	t.Run("InvalidJSONResponse", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{ this is not valid JSON }`))
		}))
		defer ts.Close()

		_, err := loadConfigFromURL(ctx, client, ts.URL, "", "", 1)
		if err == nil {
			t.Errorf("Expected error for invalid JSON response, got none")
		}
	})
	t.Run("InvalidURL", func(t *testing.T) {
		_, err := loadConfigFromURL(ctx, client, "://invalid-url", "", "", 1)
		if err == nil || !strings.Contains(err.Error(), "failed to create request") {
			t.Errorf("Expected request creation error, got: %v", err)
		}
	})
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name        string
		configFile  string
		configURL   string
		expectError bool
	}{
		{"Valid local config", "config.json", "", false},
		{"Valid remote config", "", "http://example.com/config.json", false},
		{"Both config file and URL", "config.json", "http://example.com/config.json", true},
		{"No config provided", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag.CommandLine = flag.NewFlagSet(tt.name, flag.ContinueOnError)
			os.Args = []string{"cmd", "--config-file=" + tt.configFile, "--config-url=" + tt.configURL}

			_, _, err := parseFlags()

			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
			}
		})
	}
}

func parseFlags() (string, string, error) {
	var (
		configFile = flag.String("config-file", "", "Path to a local JSON configuration file")
		configURL  = flag.String("config-url", "", "URL to a remote JSON configuration file")
	)
	flag.Parse()

	// Check if both configFile and configURL are empty
	if *configFile == "" && *configURL == "" {
		return "", "", fmt.Errorf("no configuration provided. Use --config-file or --config-url")
	}

	// Check if more than one configuration option is provided
	if (*configFile != "" && *configURL != "") || (*configFile != "" && len(flag.Args()) > 0) || (*configURL != "" && len(flag.Args()) > 0) {
		return "", "", fmt.Errorf("only one of --config-file, --config-url, or raw config params can be specified")
	}

	return *configFile, *configURL, nil
}

func TestValidateConfigFlags(t *testing.T) {
	tests := []struct {
		name        string
		configFile  string
		configURL   string
		extraArgs   []string
		expectError bool
	}{
		{"No config", "", "", nil, true},
		{"Valid local config", "config.json", "", nil, false},
		{"Valid remote config", "", "http://example.com/config.json", nil, false},
		{"Both config file and URL", "config.json", "http://example.com/config.json", nil, true},
		{"Config file with extra args", "config.json", "", []string{"extraArg"}, true},
		{"Config URL with extra args", "", "http://example.com/config.json", []string{"extraArg"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag.CommandLine = flag.NewFlagSet(tt.name, flag.ContinueOnError)
			var output bytes.Buffer
			flag.CommandLine.SetOutput(&output)

			configFile := flag.String("config-file", "", "Path to config file")
			configURL := flag.String("config-url", "", "URL to config")

			args := []string{"cmd"}
			if tt.configFile != "" {
				args = append(args, "--config-file="+tt.configFile)
			}
			if tt.configURL != "" {
				args = append(args, "--config-url="+tt.configURL)
			}
			args = append(args, tt.extraArgs...)

			os.Args = args
			flag.CommandLine.Parse(args[1:])

			err := ValidateConfigFlags(configFile, configURL)

			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
			}

			if err != nil {
				expected := ""
				if tt.configFile == "" && tt.configURL == "" {
					expected = "no configuration provided, either --config-file or --config-url must be specified"
				} else {
					expected = "only one of --config-file or --config-url"
				}

				if !strings.Contains(err.Error(), expected) {
					t.Errorf("Unexpected error message: %v", err)
				}
			}
		})
	}
}

func TestRefreshConfig(t *testing.T) {
	obaServer := models.NewObaServer(
		"Test Server",
		"test-agency",
		"https://test.example.com",
		"test-key",
		[]string{"https://test.example.com/gtfs.zip"},
		[]models.GtfsRTFeed{{VehiclePositionURL: "https://test.example.com/vehicles"}},
	)

	cfg := NewConfig(
		4000,
		"testing",
		[]models.ObaServer{*obaServer},
	)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	testLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var serverHitCount atomic.Int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHitCount.Add(1)

		user, pass, hasAuth := r.BasicAuth()
		if hasAuth && (user != "testuser" || pass != "testpass") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[
					{
							"name": "Refreshed Test Server",
							"oba_base_url": "https://refreshed.example.com",
							"oba_api_key": "refreshed-key",
							"gtfs_urls": ["https://refreshed.example.com/gtfs.zip"],
							"gtfs_rt_feeds": [{"vehicle_position_url": "https://refreshed.example.com/vehicles"}],
							"agency_id": "agency-999"
					}
			]`)
	}))
	defer mockServer.Close()

	originalConfig := make([]models.ObaServer, len(cfg.Servers))
	copy(originalConfig, cfg.Servers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callbackServers []models.ObaServer
	go refreshConfig(ctx, client, mockServer.URL, "testuser", "testpass", cfg, testLogger, 100*time.Millisecond, 1, func(servers []models.ObaServer) {
		callbackServers = servers
	})

	time.Sleep(200 * time.Millisecond)

	if serverHitCount.Load() == 0 {
		t.Fatal("Mock server was never called")
	}

	if len(callbackServers) == 0 {
		t.Fatal("onUpdated callback was never invoked with refreshed servers")
	}

	updatedServers := cfg.GetServers()

	if len(updatedServers) == 0 {
		t.Fatal("No servers found in updated configuration")
	}

	var found bool
	for _, s := range updatedServers {
		if s.AgencyID == "agency-999" && s.Name == "Refreshed Test Server" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Config not updated with refreshed server data. Original: %+v, Updated: %+v", originalConfig, updatedServers)
	}
}
