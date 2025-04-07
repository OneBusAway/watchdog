package utils

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetLastCachedFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	hash1 := sha1.Sum([]byte("https://example.com/gtfs1"))
	hashStr1 := hex.EncodeToString(hash1[:])
	createFileWithModTime(t, filepath.Join(tmpDir, fmt.Sprintf("server_1_%s.zip", hashStr1)), time.Now().Add(-2*time.Hour))
	createFileWithModTime(t, filepath.Join(tmpDir, fmt.Sprintf("server_1_%s_old.zip", hashStr1)), time.Now().Add(-3*time.Hour))

	hash2 := sha1.Sum([]byte("https://example.com/gtfs2"))
	hashStr2 := hex.EncodeToString(hash2[:])
	createFileWithModTime(t, filepath.Join(tmpDir, fmt.Sprintf("server_2_%s.zip", hashStr2)), time.Now().Add(-1*time.Hour))

	lastFile, err := GetLastCachedFile(tmpDir, 1)
	if err != nil {
		t.Fatalf("GetLastCachedFile failed: %v", err)
	}
	expectedFile := filepath.Join(tmpDir, fmt.Sprintf("server_1_%s.zip", hashStr1))
	if lastFile != expectedFile {
		t.Errorf("Expected last file for server 1 to be %s, got %s", expectedFile, lastFile)
	}

	lastFile, err = GetLastCachedFile(tmpDir, 2)
	if err != nil {
		t.Fatalf("GetLastCachedFile failed: %v", err)
	}
	expectedFile = filepath.Join(tmpDir, fmt.Sprintf("server_2_%s.zip", hashStr2))
	if lastFile != expectedFile {
		t.Errorf("Expected last file for server 2 to be %s, got %s", expectedFile, lastFile)
	}

	_, err = GetLastCachedFile(tmpDir, 3)
	if err == nil {
		t.Error("Expected an error for a server with no cached files, but got nil")
	}
}

func createFileWithModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Failed to create file %s: %v", path, err)
	}
	defer file.Close()

	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Failed to set modification time for file %s: %v", path, err)
	}
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
}
