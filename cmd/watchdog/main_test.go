package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

func TestLoadConfigFromFile(t *testing.T) {
	content := `[{
	"name": "Test Server", "id": 1,
	"oba_base_url": "https://test.example.com",
	"oba_api_key": "test-key",
	"gtfs_url": "https://gtfs.example.com",
	"trip_update_url": "https://trip.example.com",
	"vehicle_position_url": "https://vehicle.example.com",
	"gtfs_rt_api_key": "",
	"gtfs_rt_api_value": ""
	}]`
	tmpFile, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}
	tmpFile.Close()

	servers, err := loadConfigFromFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("loadConfigFromFile failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("Expected 1 server, got %d", len(servers))
	}

	expected := models.ObaServer{
		Name:               "Test Server",
		ID:                 1,
		ObaBaseURL:         "https://test.example.com",
		ObaApiKey:          "test-key",
		GtfsUrl:            "https://gtfs.example.com",
		TripUpdateUrl:      "https://trip.example.com",
		VehiclePositionUrl: "https://vehicle.example.com",
		GtfsRtApiKey:       "",
		GtfsRtApiValue:     "",
	}

	if servers[0] != expected {
		t.Errorf("Expected server %+v, got %+v", expected, servers[0])
	}
}

func TestLoadConfigFromURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"name": "Test Server",
		 "id": 1,
		 "oba_base_url": "https://test.example.com",
		 "oba_api_key": "test-key",
		 "gtfs_url": "https://gtfs.example.com",
		 "trip_update_url": "https://trip.example.com",
		 "vehicle_position_url": "https://vehicle.example.com",
		 "gtfs_rt_api_key": "",
		 "gtfs_rt_api_value": ""
		}]`))
	}))
	defer ts.Close()

	servers, err := loadConfigFromURL(ts.URL, "user", "pass")
	if err != nil {
		t.Fatalf("loadConfigFromURL failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("Expected 1 server, got %d", len(servers))
	}

	expected := models.ObaServer{
		Name:               "Test Server",
		ID:                 1,
		ObaBaseURL:         "https://test.example.com",
		ObaApiKey:          "test-key",
		GtfsUrl:            "https://gtfs.example.com",
		TripUpdateUrl:      "https://trip.example.com",
		VehiclePositionUrl: "https://vehicle.example.com",
	}

	if servers[0] != expected {
		t.Errorf("Expected server %+v, got %+v", expected, servers[0])
	}
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
