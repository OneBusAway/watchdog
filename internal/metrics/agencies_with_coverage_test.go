package metrics

import (
	"log/slog"
	"net/http"
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

func TestCheckAgenciesWithCoverage(t *testing.T) {
	reporter := report.NewReporter("test", "development")
	// Test case: Successful execution

	t.Run("Success", func(t *testing.T) {
		fixturePath := getFixturePath(t, "gtfs.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer ts.Close()

		testServer := createTestServer(ts.URL, "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer, reporter)
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

	// Test case: Error opening file
	t.Run("ErrorOpeningFile", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := CheckAgenciesWithCoverageMatch("invalid/path/to/gtfs.zip", logger, testServer, reporter)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	// Test case: Error reading file
	t.Run("ErrorReadingFile", func(t *testing.T) {
		fixturePath := getFixturePath(t, "empty.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer, reporter)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	// Test case: Error parsing GTFS data
	t.Run("ErrorParsingGTFSData", func(t *testing.T) {
		fixturePath := getFixturePath(t, "invalid_gtfs.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer, reporter)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})
}

// OBASdk tests
func TestGetAgenciesWithCoverage(t *testing.T) {
	reporter := report.NewReporter("test", "development")
	t.Run("NilResponse", func(t *testing.T) {
		ts := setupObaServer(t, `{}`, http.StatusOK)
		defer ts.Close()

		server := models.ObaServer{
			Name:       "Test Server",
			ID:         999,
			ObaBaseURL: ts.URL,
			ObaApiKey:  "test-key",
		}

		count, err := GetAgenciesWithCoverage(server, reporter)
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

		count, err := GetAgenciesWithCoverage(server, reporter)
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

		_, err := GetAgenciesWithCoverage(server, reporter)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
	})
}
