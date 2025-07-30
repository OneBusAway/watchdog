package gtfs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupGtfsServer(t *testing.T, fixturePath string) *httptest.Server {
	t.Helper()

	gtfsFixturePath := getFixturePath(t, fixturePath)

	gtfsFileData, err := os.ReadFile(gtfsFixturePath)
	if err != nil {
		t.Fatalf("Failed to read GTFS-RT fixture file: %v", err)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(gtfsFileData)
	}))
}

func getFixturePath(t *testing.T, fixturePath string) string {
	t.Helper()

	absPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixturePath))
	if err != nil {
		t.Fatalf("Failed to get absolute path to testdata/%s: %v", fixturePath, err)
	}

	return absPath
}

func readFixture(t *testing.T, fixturePath string) []byte {
	t.Helper()

	absPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixturePath))
	if err != nil {
		t.Fatalf("Failed to get absolute path to testdata/%s: %v", fixturePath, err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("Failed to read fixture file: %v", err)
	}

	return data
}
