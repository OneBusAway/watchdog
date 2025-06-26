//go:build integration
package integration

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

var integrationConfig string

func init() {
	flag.StringVar(&integrationConfig, "integration-config", "", "Path to integration configuration file")
}

var integrationServers []models.ObaServer

func TestMain(m *testing.M) {
	flag.Parse()

	if integrationConfig == "" {
		fmt.Fprintln(os.Stderr, "Error: -integration-config flag is required for integration tests")
		os.Exit(1)
	}

	data, err := os.ReadFile(integrationConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config file: %v\n%v", err ,integrationConfig )
		os.Exit(1)
	}

	if err := json.Unmarshal(data, &integrationServers); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse config JSON: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
