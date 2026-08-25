package metrics

import (
	"net/http"
	"testing"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

func TestCheckServer(t *testing.T) {
	t.Run("Successful response", func(t *testing.T) {
		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`, http.StatusOK)
		defer ts.Close()

		testServer := models.ObaServer{AgencyName: "Test Server", AgencyID: "1", ObaBaseURL: ts.URL, ObaApiKey: "test-key"}

		client := onebusaway.NewClient(
			option.WithAPIKey(testServer.ObaApiKey),
			option.WithBaseURL(testServer.ObaBaseURL),
		)
		serverPing(client, testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{

			"server_name": testServer.ServerName,
			"server_url":  utils.SanitizeServerURL(testServer.ObaBaseURL),
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

		testServer := models.ObaServer{AgencyName: "Test Server No Time", AgencyID: "2", ObaBaseURL: ts.URL, ObaApiKey: "test-key"}

		client := onebusaway.NewClient(
			option.WithAPIKey(testServer.ObaApiKey),
			option.WithBaseURL(testServer.ObaBaseURL),
		)
		serverPing(client, testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{

			"server_name": testServer.ServerName,
			"server_url":  utils.SanitizeServerURL(testServer.ObaBaseURL),
		})
		if err != nil {
			t.Fatal(err)
		}

		if metricValue != 0 {
			t.Errorf("Expected metric value to be 0 (missing readableTime), got %v", metricValue)
		}
	})

	t.Run("HTTP request failure", func(t *testing.T) {
		testServer := models.ObaServer{AgencyName: "Test Server Invalid", AgencyID: "3", ObaBaseURL: "http://invalid.url", ObaApiKey: "test-key"}

		client := onebusaway.NewClient(
			option.WithAPIKey(testServer.ObaApiKey),
			option.WithBaseURL(testServer.ObaBaseURL),
		)
		serverPing(client, testServer)
		time.Sleep(100 * time.Millisecond)

		metricValue, err := getMetricValue(ObaApiStatus, map[string]string{

			"server_name": testServer.ServerName,
			"server_url":  utils.SanitizeServerURL(testServer.ObaBaseURL),
		})
		if err != nil {
			t.Fatal(err)
		}

		if metricValue != 0 {
			t.Errorf("Expected metric value to be 0 (request failure), got %v", metricValue)
		}
	})
}
