//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"

	"watchdog.onebusaway.org/internal/models"
)

const integrationConfigPath = "./integration_servers.json"


// loadIntegrationServers loads servers data from integration_servers.json file.
func loadIntegrationServers() ([]models.ObaServer, error) {
	data, err := os.ReadFile(integrationConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var servers []models.ObaServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return servers, nil
}
