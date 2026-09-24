package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"net/http"
	"net/http/httptest"
)

func TestRun_HelpAndVersion(t *testing.T) {
	err := run(context.Background(), []string{"-version"})
	if err != nil {
		t.Fatalf("expected nil error for version, got %v", err)
	}

	err = run(context.Background(), []string{"-h"})
	if err == nil {
		t.Fatalf("expected error for help, got nil")
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	err := run(context.Background(), []string{"-config-file", "nonexistent.json"})
	if err == nil {
		t.Fatalf("expected error for nonexistent config")
	}
}

func TestRun_ValidConfigButEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(`[]`), 0644); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), []string{"-config-file", configFile})
	if err == nil || err.Error() != "no servers found in configuration" {
		t.Fatalf("expected 'no servers found in configuration', got %v", err)
	}
}

func TestRun_FullStartupAndShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(`[{
		"server_name": "Test Server",
		"agency_name": "Test Server",
		"oba_base_url": "https://test.example.com",
		"oba_api_key": "test-key",
		"gtfs_static_feeds": ["https://gtfs.example.com"],
		"gtfs_rt_feeds": [{"trip_update_url": "https://trip.example.com", "vehicle_position_url": "https://vehicle.example.com"}],
		"agency_id": "agency-1"
	}]`), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config-file", configFile, "-port", "0"})
	}()

	// Wait for server to start
	time.Sleep(200 * time.Millisecond)

	// Send cancel
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for run to exit")
	}
}

func TestRun_ConfigURL(t *testing.T) {
	content := `[{
		"server_name": "Test Server",
		"agency_name": "Test Server",
		"oba_base_url": "https://test.example.com",
		"oba_api_key": "test-key",
		"gtfs_static_feeds": ["https://gtfs.example.com"],
		"gtfs_rt_feeds": [{"trip_update_url": "https://trip.example.com", "vehicle_position_url": "https://vehicle.example.com"}],
		"agency_id": "agency-1"
	}]`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config-url", server.URL, "-port", "0"})
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
}
