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

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

func TestDownloadGTFSBundles(t *testing.T) {
	servers := []models.ObaServer{
		{ID: 1, GtfsUrl: "https://example.com/gtfs.zip"},
	}

	tempDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reporter := report.NewReporter("test", "development")

	DownloadGTFSBundles(servers, tempDir, logger, reporter)

}

func TestRefreshGTFSBundles(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reporter := report.NewReporter("test", "development")

	servers := []models.ObaServer{{ID: 1, Name: "Test Server", GtfsUrl: "http://example.com/gtfs.zip"}}
	cacheDir := t.TempDir()

	go RefreshGTFSBundles(servers, cacheDir, logger, 10*time.Millisecond, reporter)

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
