package gtfs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/OneBusAway/go-gtfs"
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

func assertVehicle(t *testing.T, actual *gtfs.Vehicle, expected *gtfs.Vehicle) {
	t.Helper()

	if expected.ID != nil {
		if actual.ID == nil {
			t.Errorf("vehicle ID missing")
		} else if expected.ID.ID != actual.ID.ID {
			t.Errorf("vehicle ID mismatch: expected %s, got %s",
				expected.ID.ID, actual.ID.ID)
		}
	}

	if expected.StopID != nil {
		if actual.StopID == nil {
			t.Errorf("vehicle StopID missing")
		} else if *expected.StopID != *actual.StopID {
			t.Errorf("vehicle StopID mismatch: expected %s, got %s",
				*expected.StopID, *actual.StopID)
		}
	}

	if expected.CurrentStopSequence != nil {
		if actual.CurrentStopSequence == nil {
			t.Errorf("vehicle StopSequence missing")
		} else if *expected.CurrentStopSequence != *actual.CurrentStopSequence {
			t.Errorf("vehicle StopSequence mismatch: expected %d, got %d",
				*expected.CurrentStopSequence, *actual.CurrentStopSequence)
		}
	}

	if expected.Timestamp != nil {
		if actual.Timestamp == nil {
			t.Errorf("vehicle Timestamp missing")
		} else if !expected.Timestamp.Equal(*actual.Timestamp) {
			t.Errorf("vehicle Timestamp mismatch")
		}
	}

	if expected.Position != nil {
		if actual.Position == nil {
			t.Errorf("vehicle Position missing")
		} else {
			const eps = 1e-5

			if expected.Position.Latitude != nil && actual.Position.Latitude != nil {
				if diff := *expected.Position.Latitude - *actual.Position.Latitude; diff > eps || diff < -eps {
					t.Errorf("latitude mismatch: expected %f got %f",
						*expected.Position.Latitude, *actual.Position.Latitude)
				}
			}

			if expected.Position.Longitude != nil && actual.Position.Longitude != nil {
				if diff := *expected.Position.Longitude - *actual.Position.Longitude; diff > eps || diff < -eps {
					t.Errorf("longitude mismatch: expected %f got %f",
						*expected.Position.Longitude, *actual.Position.Longitude)
				}
			}

			if expected.Position.Speed != nil && actual.Position.Speed != nil {
				if diff := *expected.Position.Speed - *actual.Position.Speed; diff > eps || diff < -eps {
					t.Errorf("speed mismatch: expected %f got %f",
						*expected.Position.Speed, *actual.Position.Speed)
				}
			}
		}
	}

	if expected.IsEntityInMessage != actual.IsEntityInMessage {
		t.Errorf("IsEntityInMessage mismatch: expected %v got %v",
			expected.IsEntityInMessage, actual.IsEntityInMessage)
	}
}
