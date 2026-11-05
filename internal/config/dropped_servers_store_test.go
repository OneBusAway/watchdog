package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

func TestDroppedServersStore_SharedIdentityRecovery(t *testing.T) {
	store := NewDroppedServersStore()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// 1. Initial state: "bad" server is malformed (missing API key)
	brokenJSON := []json.RawMessage{
		json.RawMessage(`{
			"server_name": "bad",
			"agency_name": "MBTA",
			"agency_id": "1",
			"oba_base_url": "http://api.pugetsound.onebusaway.org",
			"gtfs_static_feeds": ["https://cdn.mbta.com/MBTA_GTFS.zip"]
		}`),
	}

	validServers := store.Reconcile(brokenJSON, logger)
	if len(validServers) != 0 {
		t.Fatalf("expected 0 valid servers, got %d", len(validServers))
	}

	// Verify it was recorded with the original "bad" tags
	identity := string(models.ServerKey("http://api.pugetsound.onebusaway.org", "1"))
	tags, ok := store.reported[identity]
	if !ok {
		t.Fatalf("expected server to be in reported store")
	}
	if tags["server_name"] != "bad" {
		t.Errorf("expected reported tag to be 'bad', got %q", tags["server_name"])
	}

	// 2. Recovery state: a new valid server "a" shares the same identity
	fixedJSON := []json.RawMessage{
		json.RawMessage(`{
			"server_name": "a",
			"agency_name": "MBTA",
			"agency_id": "1",
			"oba_base_url": "http://api.pugetsound.onebusaway.org",
			"oba_api_key": "TEST",
			"gtfs_static_feeds": ["https://cdn.mbta.com/MBTA_GTFS.zip"],
			"gtfs_rt_feeds": [{"trip_update_url": "http://test", "vehicle_position_url": "http://test"}]
		}`),
	}

	validServers = store.Reconcile(fixedJSON, logger)
	if len(validServers) != 1 {
		t.Fatalf("expected 1 valid server, got %d", len(validServers))
	}

	// Verify it was cleared from the reported store
	if _, ok := store.reported[identity]; ok {
		t.Errorf("expected server to be cleared from reported store after recovery")
	}
}
