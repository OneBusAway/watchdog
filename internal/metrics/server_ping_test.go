package metrics

import (
	"net/http"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestCheckServer(t *testing.T) {
	t.Run("Successful response", func(t *testing.T) {
		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`, http.StatusOK)
		defer ts.Close()

		testServer := models.ObaServer{Name: "Test Server", AgencyID: "1", ObaBaseURL: ts.URL, ObaApiKey: "test-key"}

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"agency_id":  testServer.AgencyID,
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

		testServer := models.ObaServer{Name: "Test Server No Time", AgencyID: "2", ObaBaseURL: ts.URL, ObaApiKey: "test-key"}

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"agency_id":  testServer.AgencyID,
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
		testServer := models.ObaServer{Name: "Test Server Invalid", AgencyID: "3", ObaBaseURL: "http://invalid.url", ObaApiKey: "test-key"}

		serverPing(testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{
			"agency_id":  testServer.AgencyID,
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
