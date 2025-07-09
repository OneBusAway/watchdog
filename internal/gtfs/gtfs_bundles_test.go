package gtfs

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestDownloadGTFSBundles(t *testing.T) {
	servers := []models.ObaServer{
		{ID: 1, GtfsUrl: "https://example.com/gtfs.zip"},
	}

	tempDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := geo.NewBoundingBoxStore()

	DownloadGTFSBundles(servers, tempDir, logger, store)

}

func TestRefreshGTFSBundles(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	servers := []models.ObaServer{{ID: 1, Name: "Test Server", GtfsUrl: "http://example.com/gtfs.zip"}}
	cacheDir := t.TempDir()
	store := geo.NewBoundingBoxStore()

	go RefreshGTFSBundles(servers, cacheDir, logger, 10*time.Millisecond, store)

	time.Sleep(15 * time.Millisecond)

	t.Log("refreshGTFSBundles executed without crashing")
}

func TestDownloadGTFSBundle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mock GTFS data"))
	}))
	defer mockServer.Close()

	serverID := 1
	hash := sha1.Sum([]byte(mockServer.URL))
	hashStr := hex.EncodeToString(hash[:])
	cachePath, err := DownloadGTFSBundle(mockServer.URL, tmpDir, serverID, hashStr)
	if err != nil {
		t.Fatalf("DownloadGTFSBundle failed: %v", err)
	}

	expectedFileName := fmt.Sprintf("server_%d_%s.zip", serverID, hashStr)
	expectedFilePath := filepath.Join(tmpDir, expectedFileName)
	if cachePath != expectedFilePath {
		t.Errorf("Expected cache path to be %s, got %s", expectedFilePath, cachePath)
	}

	fileContent, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("Failed to read downloaded file: %v", err)
	}

	expectedContent := "mock GTFS data"
	if string(fileContent) != expectedContent {
		t.Errorf("Expected file content to be %s, got %s", expectedContent, string(fileContent))
	}

	serverID = 2
	hash = sha1.Sum([]byte(mockServer.URL))
	hashStr = hex.EncodeToString(hash[:])
	cachePath, err = DownloadGTFSBundle(mockServer.URL, tmpDir, serverID, hashStr)
	if err != nil {
		t.Fatalf("DownloadGTFSBundle failed: %v", err)
	}

	expectedFileName = fmt.Sprintf("server_%d_%s.zip", serverID, hashStr)
	expectedFilePath = filepath.Join(tmpDir, expectedFileName)
	if cachePath != expectedFilePath {
		t.Errorf("Expected cache path to be %s, got %s", expectedFilePath, cachePath)
	}

	t.Run("Invalid URL", func(t *testing.T) {
		invalidURL := "http://invalid-url"
		_, err := DownloadGTFSBundle(invalidURL, tmpDir, 3, "invalidhash")
		if err == nil {
			t.Errorf("Expected error for invalid URL, got none")
		}
	})

	t.Run("Invalid Cache Directory", func(t *testing.T) {
		invalidDir := "/invalid/cache/dir"
		_, err := DownloadGTFSBundle(mockServer.URL, invalidDir, 4, "invalidhash")
		if err == nil {
			t.Errorf("Expected error for invalid cache directory, got none")
		}
	})
	t.Run("IO Copy Failure", func(t *testing.T) {
		mockServerFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
		}))
		defer mockServerFailure.Close()

		_, err := DownloadGTFSBundle(mockServerFailure.URL, tmpDir, 5, "hashIOFail")
		if err == nil {
			t.Errorf("Expected error for io.Copy failure, got none")
		}
	})
}

func TestGetStopLocationsByIDs(t *testing.T) {
	cachePath := "../../testdata/gtfs.zip"
	server := models.ObaServer{ID: 1, Name: "test"}

	t.Run("Valid stop IDs", func(t *testing.T) {
		stopIDs := []string{"11060", "1108"} // Make sure these exist in your test GTFS
		stops, err := GetStopLocationsByIDs(cachePath, server.ID, stopIDs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) == 0 {
			t.Fatalf("expected some matched stops, got 0")
		}
	})

	t.Run("Invalid stop IDs", func(t *testing.T) {
		stopIDs := []string{"nonexistent1", "nonexistent2"}
		stops, err := GetStopLocationsByIDs(cachePath, server.ID, stopIDs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) != 0 {
			t.Errorf("expected 0 matched stops, got %d", len(stops))
		}
	})
}

func TestParseGTFSFromCache(t *testing.T) {
	server := models.ObaServer{ID: 1, Name: "Test"}

	tests := []struct {
		name      string
		cachePath string
		expectErr bool
	}{
		{
			name:      "Valid GTFS file",
			cachePath: "../../testdata/gtfs.zip",
			expectErr: false,
		},
		{
			name:      "Invalid path",
			cachePath: "../../testdata/does-not-exist.zip",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			staticData, err := ParseGTFSFromCache(tc.cachePath, server.ID)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if staticData == nil {
				t.Fatal("expected staticData to be non-nil")
			}
			if len(staticData.Stops) == 0 {
				t.Error("expected at least one stop in GTFS data, got 0")
			}
		})
	}
}

func TestFetchGTFSRTFeed(t *testing.T) {
	t.Run("Success Case", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Header") != "test-value" {
				t.Errorf("Expected header X-Test-Header to be set")
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("mock gtfs-rt response"))
		}))
		defer mockServer.Close()

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: mockServer.URL,
			GtfsRtApiKey:       "X-Test-Header",
			GtfsRtApiValue:     "test-value",
		}

		resp, err := FetchGTFSRTFeed(server)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "mock gtfs-rt response" {
			t.Errorf("Expected response body 'mock gtfs-rt response', got '%s'", body)
		}
	})

	t.Run("Failure Case - Invalid URL", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 2,
			VehiclePositionUrl: "://invalid-url",
		}
		_, err := FetchGTFSRTFeed(server)
		if err == nil {
			t.Error("Expected error due to invalid URL, got nil")
		}
	})

	t.Run("Failure Case - Closed Server", func(t *testing.T) {
		mockServer := httptest.NewServer(nil)
		mockServer.Close()

		server := models.ObaServer{
			ID:                 3,
			VehiclePositionUrl: mockServer.URL,
		}
		_, err := FetchGTFSRTFeed(server)
		if err == nil {
			t.Error("Expected error when accessing closed server, got nil")
		}
	})
}
