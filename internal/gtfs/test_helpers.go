package gtfs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
)

func setupGtfsServer(t *testing.T, fixturePath string) *httptest.Server {
	t.Helper()

	gtfsFixturePath := getFixturePath(t, fixturePath)

	// Safe: absPath is only used in local tests and not from user input.
	// #nosec G304
	gtfsFileData, err := os.ReadFile(gtfsFixturePath)
	if err != nil {
		t.Fatalf("Failed to read GTFS-RT fixture file: %v", err)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Writing to ResponseWriter in tests, error can be safely ignored.
		// #nosec G104
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
	// Safe: absPath is only used in local tests and not from user input.
	// #nosec G304
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("Failed to read fixture file: %v", err)
	}

	return data
}

func assertPtr[T comparable](t *testing.T, expected *T, actual *T, field string, equal func(a, b T) bool) {
	t.Helper()

	if expected == nil {
		if actual != nil {
			t.Errorf("%s: unexpected value", field)
		}
		return
	}

	if actual == nil {
		t.Errorf("%s: missing value", field)
		return
	}
	if !equal(*expected, *actual) {
		t.Errorf("%s mismatch: expected %v, got %v", field, *expected, *actual)
	}
}

func assertVehicle(t *testing.T, actual *remoteGtfs.Vehicle, expected *remoteGtfs.Vehicle) {
	t.Helper()
	if expected == nil {
		t.Fatal("expected vehicle must not be nil")
	}
	if actual == nil {
		t.Errorf("actual vehicle is nil")
		return

	}

	if expected.ID == nil {
		if actual.ID != nil {
			t.Errorf("mismatch between expected vehicle ID and actual vehicle ID")
		}
	} else {
		if actual.ID == nil {
			t.Errorf("vehicle ID missing")
		} else if expected.ID.ID != actual.ID.ID {
			t.Errorf("vehicle ID mismatch: expected %s, got %s",
				expected.ID.ID, actual.ID.ID)
		}
	}

	assertPtr(t, expected.StopID, actual.StopID, "vehicle StopID", func(a, b string) bool {
		return a == b
	})
	assertPtr(t, expected.CurrentStopSequence, actual.CurrentStopSequence, "vehicle StopSequence", func(a, b uint32) bool {
		return a == b
	})
	assertPtr(t, expected.Timestamp, actual.Timestamp, "vehicle Timestamp", func(a, b time.Time) bool {
		return a.Equal(b)
	})

	if expected.Position == nil {
		if actual.Position != nil {
			t.Errorf("vehicle Position: unexpected value")
		}
	} else {
		if actual.Position == nil {
			t.Errorf("vehicle Position: missing value")
		} else {
			const eps = 1e-5

			floatEq := func(a, b float32) bool {
				diff := a - b
				return diff <= eps && diff >= -eps
			}

			assertPtr(t, expected.Position.Latitude, actual.Position.Latitude, "vehicle latitude", floatEq)
			assertPtr(t, expected.Position.Longitude, actual.Position.Longitude, "vehicle longitude", floatEq)
			assertPtr(t, expected.Position.Speed, actual.Position.Speed, "vehicle speed", floatEq)
		}
	}

	if expected.IsEntityInMessage != actual.IsEntityInMessage {
		t.Errorf("IsEntityInMessage mismatch: expected %v got %v",
			expected.IsEntityInMessage, actual.IsEntityInMessage)
	}
}
