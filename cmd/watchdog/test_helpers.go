package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/server"

	"watchdog.onebusaway.org/internal/models"
)

// getMetricValue is a helper function that retrieves the current value of a specific metric
func getMetricValue(metric *prometheus.GaugeVec, labels map[string]string) (float64, error) {
	// Create a collector for our specific metric
	c := make(chan prometheus.Metric, 1)
	metric.With(labels).Collect(c)

	// Get the metric from the channel
	m := <-c

	// Create a DESC and value for our metric
	var metricValue float64
	pb := &dto.Metric{}
	if err := m.Write(pb); err != nil {
		return 0, err
	}

	if pb.Gauge != nil {
		metricValue = pb.Gauge.GetValue()
	}

	return metricValue, nil
}

func newTestApplication(t *testing.T) *application {
	t.Helper()

	obaServer := models.NewObaServer(
		"Test Server",
		1,
		"https://test.example.com",
		"test-key",
		"",
		"",
		"",
		"",
		"",
		"",
	)

	cfg := server.NewConfig(
		4000,
		"testing",
		[]models.ObaServer{*obaServer},
	)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	return &application{
		config: *cfg,
		logger: logger,
	}
}

func getMetricsForTesting(t *testing.T, metric *prometheus.GaugeVec) {
	t.Helper()

	ch := make(chan prometheus.Metric)
	go func() {
		metric.Collect(ch)
		close(ch)
	}()

	for m := range ch {
		t.Logf("Found metric: %v", m.Desc())
	}
}

func setupGtfsRtServer(t *testing.T, fixturePath string) *httptest.Server {
	t.Helper()

	gtfsRtFixturePath := getFixturePath(t, fixturePath)

	gtfsRtFileData, err := os.ReadFile(gtfsRtFixturePath)
	if err != nil {
		t.Fatalf("Failed to read GTFS-RT fixture file: %v", err)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(gtfsRtFileData)
	}))
}

func setupObaServer(t *testing.T, response string, statusCode int) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write([]byte(response))
	}))
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

func getFixturePath(t *testing.T, fixturePath string) string {
	t.Helper()

	absPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixturePath))
	if err != nil {
		t.Fatalf("Failed to get absolute path to testdata/%s: %v", fixturePath, err)
	}

	return absPath
}

func createTestServer(url, name string, id int, apiKey string, vehiclePositionUrl string, gtfsRtApiKey string, gtfsRtApiValue string, agencyID string) models.ObaServer {
	return models.ObaServer{
		Name:               name,
		ID:                 id,
		ObaBaseURL:         url,
		VehiclePositionUrl: vehiclePositionUrl,
		ObaApiKey:          apiKey,
		GtfsRtApiKey:       gtfsRtApiKey,
		GtfsRtApiValue:     gtfsRtApiValue,
		AgencyID:           agencyID,
	}
}
func setupTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(func() { ts.Close() })
	return ts
}
