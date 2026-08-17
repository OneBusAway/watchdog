package config

import (
	"reflect"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

func TestNewConfig(t *testing.T) {
	// Test cases
	tests := []struct {
		name         string
		port         int
		env          string
		servers      []models.ObaServer
		expectedPort int
		expectedEnv  string
	}{
		{
			name: "Valid configuration with one server",
			port: 4000,
			env:  "development",
			servers: []models.ObaServer{
				{
					AgencyName:  "Test Server",
					ObaBaseURL:  "https://test.onebusaway.org",
					ObaApiKey:   "test-key",
					AgencyID:    "agency-1",
					GtfsURLs:    []string{"https://test.gtfs.url"},
					GtfsRTFeeds: []models.GtfsRTFeed{{TripUpdateURL: "https://test.update.url", VehiclePositionURL: "https://test.vehicle.url"}},
				},
			},
			expectedPort: 4000,
			expectedEnv:  "development",
		},
		{
			name:         "Empty server list",
			port:         8080,
			env:          "production",
			servers:      []models.ObaServer{},
			expectedPort: 8080,
			expectedEnv:  "production",
		},
		{
			name: "Multiple servers",
			port: 3000,
			env:  "staging",
			servers: []models.ObaServer{
				{
					AgencyName: "Server 1",
					ObaBaseURL: "https://test1.onebusaway.org",
					AgencyID:   "agency-1",
				},
				{
					AgencyName: "Server 2",
					ObaBaseURL: "https://test2.onebusaway.org",
					AgencyID:   "agency-2",
				},
			},
			expectedPort: 3000,
			expectedEnv:  "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create new config
			config := NewConfig(tt.port, tt.env, tt.servers)

			// Check if config is not nil
			if config == nil {
				t.Fatal("Expected config to not be nil")
			}

			// Check port
			if config.Port != tt.expectedPort {
				t.Errorf("Expected port %d, got %d", tt.expectedPort, config.Port)
			}

			// Check environment
			if config.Env != tt.expectedEnv {
				t.Errorf("Expected environment %s, got %s", tt.expectedEnv, config.Env)
			}

			// Check servers
			if !reflect.DeepEqual(config.Servers, tt.servers) {
				t.Errorf("Servers don't match expected values.\nExpected: %+v\nGot: %+v", tt.servers, config.Servers)
			}

			// Check server count
			if len(config.Servers) != len(tt.servers) {
				t.Errorf("Expected %d servers, got %d", len(tt.servers), len(config.Servers))
			}
		})
	}
}

func TestConfigFields(t *testing.T) {
	// Test that the Config struct has all expected fields
	configType := reflect.TypeOf(Config{})

	expectedFields := map[string]string{
		"Port":    "int",
		"Env":     "string",
		"Servers": "[]models.ObaServer",
	}

	for fieldName, expectedType := range expectedFields {
		field, exists := configType.FieldByName(fieldName)
		if !exists {
			t.Errorf("Expected Config struct to have field %s", fieldName)
			continue
		}

		actualType := field.Type.String()
		if actualType != expectedType {
			t.Errorf("Field %s: expected type %s, got %s", fieldName, expectedType, actualType)
		}
	}
}

func TestUpdateConfig(t *testing.T) {
	initialServers := []models.ObaServer{
		{AgencyID: "agency-1", AgencyName: "Server 1"},
	}
	config := NewConfig(1, "testing", initialServers)

	newServers := []models.ObaServer{
		{AgencyID: "agency-1", AgencyName: "Server 1 Updated"},
		{AgencyID: "agency-2", AgencyName: "Server 2"},
	}

	config.UpdateConfig(newServers)

	if len(config.GetServers()) != 2 {
		t.Errorf("Expected 2 servers, got %d", len(config.Servers))
	}

	if config.Servers[0].AgencyName != "Server 1 Updated" {
		t.Errorf("Expected server name to be updated to 'Server 1 Updated', got %s", config.Servers[0].AgencyName)
	}
}
