package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/metrics"
)

func TestMetricsEndpoint(t *testing.T) {
	// Create a new instance of our application
	app := newTestApplication(t)

	// Register the metric without starting the collection routine
	metrics.ObaApiStatus.WithLabelValues("1", "https://test.example.com").Set(1)
	// Create a test server
	ts := httptest.NewServer(app.routes())
	defer ts.Close()
	// Make a request to the metrics endpoint
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Check status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want %d; got %d", http.StatusOK, resp.StatusCode)
	}
	// Check that the response contains our metric
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), "oba_api_status") {
		t.Error("metrics response doesn't contain oba_api_status metric")
	}
}

func TestCollectMetricsForServer(t *testing.T) {
	app := newTestApplication(t)

	prometheus.DefaultRegisterer = prometheus.NewRegistry()

	testServer := app.config.Servers[0]

	app.collectMetricsForServer(testServer)

	getMetricsForTesting(t, metrics.ObaApiStatus)
}
