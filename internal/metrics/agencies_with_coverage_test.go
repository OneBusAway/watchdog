package metrics

import (
	"log/slog"
	"net/http"
	"os"
	"testing"

	remoteGtfs "github.com/jamespfennell/gtfs"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestCheckAgenciesWithCoverage(t *testing.T) {
	// Test case: Successful execution

	t.Run("Success", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer ts.Close()

		testServer := createTestServer(ts.URL, "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		data := readFixture(t, "gtfs.zip")
		staticData, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
		if err != nil {
			t.Fatal("failed to parse gtfs static data")
		}
		staticStore := gtfs.NewStaticStore()
		staticStore.Set(testServer.ID, staticData)

		err = checkAgenciesWithCoverageMatch(staticStore, logger, testServer)
		if err != nil {
			t.Fatalf("CheckAgenciesWithCoverageMatch failed: %v", err)
		}

		agencyMatchMetric, err := getMetricValue(AgenciesMatch, map[string]string{"server_id": "999"})
		if err != nil {
			t.Errorf("Failed to get AgenciesMatch metric value: %v", err)
		}

		if agencyMatchMetric != 1 {
			t.Errorf("Expected agency match metric to be 1, got %v", agencyMatchMetric)
		}
	})
}

// OBASdk tests
func TestGetAgenciesWithCoverage(t *testing.T) {
	t.Run("NilResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{}`, http.StatusOK)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
		}

		count, err := getAgenciesWithCoverage(server)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if count != 0 {
			t.Fatalf("Expected count to be 0, got %d", count)
		}
	})

	t.Run("SuccessfulResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{"data": {"list": [{"agencyId": "1"}, {"agencyId": "2"}]}}`, http.StatusOK)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
		}

		count, err := getAgenciesWithCoverage(server)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if count != 2 {
			t.Fatalf("Expected count to be 2, got %d", count)
		}
	})

	t.Run("ErrorResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
		}

		_, err := getAgenciesWithCoverage(server)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
	})
}
