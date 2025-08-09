//go:build integration

// Package integration contains integration tests that validate the
// application's behavior against real or fully simulated environments.
//
// These tests require a configuration file provided via the -integration-config flag.

package integration

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)
// integrationConfig stores the path to the integration configuration file,
// passed using the -integration-config flag at test runtime.
var integrationConfig string

// init registers the -integration-config flag used to specify the path
// to the integration test configuration file.
func init() {
	flag.StringVar(&integrationConfig, "integration-config", "", "Path to integration configuration file")
}

// integrationServers holds the list of OBA servers loaded from the config file.
// It is populated in TestMain and used by integration test cases.
var integrationServers []models.ObaServer

// TestMain handles setup before running integration tests.
// It ensures the -integration-config flag is provided,
// reads the config file, and unmarshals it into integrationServers.
func TestMain(m *testing.M) {
	flag.Parse()

	if integrationConfig == "" {
		fmt.Fprintln(os.Stderr, "Error: -integration-config flag is required for integration tests")
		os.Exit(1)
	}

	data, err := os.ReadFile(integrationConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config file: %v\n", err)
		os.Exit(1)
	}

	if err := json.Unmarshal(data, &integrationServers); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse config JSON: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}
