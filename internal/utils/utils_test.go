package utils

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
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
	t.Run("Invalid Cache Directory Read", func(t *testing.T) {
		invalidDir := "/invalid/cache/dir"
		_, err := GetLastCachedFile(invalidDir, 1)
		if err == nil {
			t.Errorf("Expected error for os.ReadDir failure, got none")
		}
	})

	t.Run("Empty Cache Directory", func(t *testing.T) {
		emptyDir, err := os.MkdirTemp("", "emptycache")
		if err != nil {
			t.Fatalf("Failed to create empty temporary directory: %v", err)
		}
		defer os.RemoveAll(emptyDir)

		_, err = GetLastCachedFile(emptyDir, 2)
		if err == nil {
			t.Errorf("Expected error for empty cache directory, but got none")
		}
	})
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

func TestCreateCacheDirectory(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("Creates new directory", func(t *testing.T) {
		baseTempDir := t.TempDir()
		tempDir := filepath.Join(baseTempDir, "test-cache")

		err := CreateCacheDirectory(tempDir, logger)
		if err != nil {
			t.Fatalf("Failed to create cache directory: %v", err)
		}

		stat, err := os.Stat(tempDir)
		if err != nil {
			t.Fatalf("Failed to stat directory: %v", err)
		}
		if !stat.IsDir() {
			t.Error("Cache directory was created but is not a directory")
		}
	})

	t.Run("Handles existing directory", func(t *testing.T) {
		baseTempDir := t.TempDir()
		tempDir := filepath.Join(baseTempDir, "test-cache")

		if err := os.MkdirAll(tempDir, os.ModePerm); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		err := CreateCacheDirectory(tempDir, logger)
		if err != nil {
			t.Errorf("Failed on existing directory: %v", err)
		}
	})

	t.Run("Fails: if path is a file", func(t *testing.T) {
		baseTempDir := t.TempDir()
		filePath := filepath.Join(baseTempDir, "test-file")

		if file, err := os.Create(filePath); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		} else {
			file.Close()
		}

		err := CreateCacheDirectory(filePath, logger)
		if err == nil {
			t.Error("Expected error when path is a file, but got nil")
		}
	})

}
