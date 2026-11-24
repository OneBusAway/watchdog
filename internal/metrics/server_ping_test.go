package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
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
		serverPing(context.Background(), client, testServer)
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
		serverPing(context.Background(), client, testServer)
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
		serverPing(context.Background(), client, testServer)
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

func TestServerPingSentryContextSanitizesObaBaseURL(t *testing.T) {
	rec := report.CaptureSentry(t)
	ts := setupObaServer(t, `{"code":500,"text":"failure","version":2}`, http.StatusInternalServerError)
	defer ts.Close()

	testServer := models.ObaServer{
		ServerName: "ping-context",
		ObaBaseURL: "https://url-user:url-password@oba.example.com/api?token=url-token",
		ObaApiKey:  "api-key-secret",
	}
	client := onebusaway.NewClient(
		option.WithAPIKey(testServer.ObaApiKey),
		option.WithBaseURL(ts.URL),
	)
	serverPing(context.Background(), client, testServer)

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 report, got %d", len(events))
	}
	event := events[0]
	extra, ok := event.Contexts["extra"]
	if !ok {
		t.Fatalf("expected extra context, got %v", event.Contexts)
	}
	if got, want := extra["oba_base_url"], "https://oba.example.com/api"; got != want {
		t.Errorf("oba_base_url context = %v, want %q", got, want)
	}

	for _, secret := range []string{"url-user", "url-password", "url-token", "api-key-secret"} {
		if strings.Contains(event.Message, secret) || strings.Contains(fmt.Sprint(event.Exception), secret) {
			t.Errorf("report leaked credential %q in message or exception", secret)
		}
		for _, tag := range event.Tags {
			if strings.Contains(tag, secret) {
				t.Errorf("report leaked credential %q in tag", secret)
			}
		}
		for contextName, context := range event.Contexts {
			for key, value := range context {
				if strings.Contains(fmt.Sprint(value), secret) {
					t.Errorf("report leaked credential %q in context %q field %q", secret, contextName, key)
				}
			}
		}
	}
}
