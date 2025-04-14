package metrics

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestCheckBundleExpiration(t *testing.T) {
	fixturePath := getFixturePath(t, "gtfs.zip")
	fixedTime := time.Date(2025, 1, 12, 20, 16, 38, 0, time.UTC)

	testServer := createTestServer("www.example.com", "Test Server", 999, "", "www.example.com", "test-api-value", "test-api-key", "1")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	earliest, latest, err := CheckBundleExpiration(fixturePath, logger, fixedTime, testServer)
	if err != nil {
		t.Fatalf("CheckBundleExpiration failed: %v", err)
	}

	// This is the current gtfs.zip file in the testdata directory expected earliest and latest expiration days
	expectedEarliest := int(time.Date(2024, 11, 22, 0, 0, 0, 0, time.UTC).Sub(fixedTime).Hours() / 24)
	expectedLatest := int(time.Date(2025, 3, 28, 0, 0, 0, 0, time.UTC).Sub(fixedTime).Hours() / 24)

	if earliest != expectedEarliest {
		t.Errorf("Expected earliest expiration days to be %d, got %d", expectedEarliest, earliest)
	}
	if latest != expectedLatest {
		t.Errorf("Expected latest expiration days to be %d, got %d", expectedLatest, latest)
	}

	earliestMetric, err := getMetricValue(BundleEarliestExpirationGauge, map[string]string{"server_id": "999"})
	if err != nil {
		t.Errorf("Failed to get earliest expiration metric value: %v", err)
	}
	if earliestMetric != float64(expectedEarliest) {
		t.Errorf("Expected earliest expiration metric to be %v, got %v", expectedEarliest, earliestMetric)
	}

	latestMetric, err := getMetricValue(BundleLatestExpirationGauge, map[string]string{"server_id": "999"})
	if err != nil {
		t.Errorf("Failed to get latest expiration metric value: %v", err)
	}
	if latestMetric != float64(expectedLatest) {
		t.Errorf("Expected latest expiration metric to be %v, got %v", expectedLatest, latestMetric)
	}
}
