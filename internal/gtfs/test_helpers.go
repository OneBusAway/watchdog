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
			t.Errorf("%s mismatch:\nexpected: nil\ngot %v", field, *actual)
		}
		return
	}

	if actual == nil {
		t.Errorf("%s mismatch:\nexpected: %v\ngot nil", field, *expected)
		return
	}
	if !equal(*expected, *actual) {
		t.Errorf("%s mismatch:\nexpected %v, got %v", field, *expected, *actual)
	}
}

func assertVehicle(t *testing.T, actual *remoteGtfs.Vehicle, expected *remoteGtfs.Vehicle) {
	t.Helper()
	if expected == nil {
		t.Fatal("expected vehicle must not be nil")
	}
	if actual == nil {
		t.Errorf("vehicle mismatch:\nexpected: %v\ngot nil", expected)
		return
	}

	if expected.ID == nil {
		if actual.ID != nil {
			t.Errorf("vehicle ID mismatch:\nexpected: nil\ngot %v", actual.ID)
		}
	} else {
		if actual.ID == nil {
			t.Errorf("vehicle ID mismatch:\nexpected: %v\ngot nil", expected.ID)
		} else if expected.ID.ID != actual.ID.ID {
			t.Errorf("vehicle ID mismatch:\nexpected: %s\ngot %s", expected.ID.ID, actual.ID.ID)
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
			t.Errorf("vehicle Position mismatch:\nexpected: nil\ngot %v", actual.Position)
		}
	} else {
		if actual.Position == nil {
			t.Errorf("vehicle Position mismatch:\nexpected: %v\ngot nil", expected.Position)
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
		t.Errorf("IsEntityInMessage mismatch:\nexpected: %v\ngot %v",
			expected.IsEntityInMessage, actual.IsEntityInMessage)
	}
}
