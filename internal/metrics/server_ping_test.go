package metrics

import (
	"net/http"
	"testing"
	"time"
)

func TestCheckServer(t *testing.T) {
	t.Run("Successful response", func(t *testing.T) {
		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`, http.StatusOK)
		defer ts.Close()

		testServer := createTestServer(ts.URL, "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"server_id":  "999",
			"server_url": testServer.ObaBaseURL,
		})
		if err != nil {
			t.Fatal(err)
		}

		if metricValue != 1 {
			t.Errorf("Expected metric value to be 1 (working), got %v", metricValue)
		}
	})

	t.Run("Response missing readableTime", func(t *testing.T) {
		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{}}}`, http.StatusOK)
		defer ts.Close()

		testServer := createTestServer(ts.URL, "Test Server No Time", 998, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"server_id":  "998",
			"server_url": testServer.ObaBaseURL,
		})
		if err != nil {
			t.Fatal(err)
		}

		if metricValue != 0 {
			t.Errorf("Expected metric value to be 0 (missing readableTime), got %v", metricValue)
		}
	})

	t.Run("HTTP request failure", func(t *testing.T) {
		testServer := createTestServer("http://invalid.url", "Test Server Invalid", 997, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"server_id":  "997",
			"server_url": testServer.ObaBaseURL,
		})
		if err != nil {
			t.Fatal(err)
		}

		if metricValue != 0 {
			t.Errorf("Expected metric value to be 0 (request failure), got %v", metricValue)
		}
	})
}
