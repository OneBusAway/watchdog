package metrics

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/models"
)

// readFixture reads the contents of a test fixture file located in the testdata directory.
// It returns the file contents as a byte slice.
// It fails the test immediately if the file cannot be read.
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

// createTestServer creates and returns a mock ObaServer instance with the given parameters.
// Useful for unit testing functions that depend on server configuration.
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

// getMetricValue retrieves the current float64 value of a Prometheus GaugeVec metric
// for the given set of labels. Returns an error if the metric cannot be parsed.
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

// setupObaServer creates a new httptest.Server that responds with the given JSON string and status code.
// Used to simulate an OBA API server for testing.
func setupObaServer(t *testing.T, response string, statusCode int) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		// Writing to ResponseWriter in tests, error can be safely ignored.
		// #nosec G104
		w.Write([]byte(response))
	}))
}

// setupTestServer creates a new httptest.Server with the provided HTTP handler.
// Automatically registers a cleanup function to close the server after the test ends.
func setupTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(func() { ts.Close() })
	return ts
}
