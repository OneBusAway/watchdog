package metrics

import (
	"bytes"
	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestFetchObaAPIMetrics_WithVCR(t *testing.T) {
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	staticData := models.NewStaticData(staticBundle)
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticStore := gtfs.NewStaticStore()

	tests := []struct {
		name      string
		slugID    string
		serverURL string
		apiKey    string
		useVCR    bool
		cassette  string
		wantErr   bool
		errString string
	}{
		{
			name:      "successful request",
			slugID:    "unitrans",
			serverURL: "https://oba-api.onrender.com",
			apiKey:    "org.onebusaway.iphone",
			useVCR:    true,
			cassette:  "oba_metrics_api_successful_request",
			wantErr:   false,
		},
		{
			name:      "not found error",
			slugID:    "invalid-region",
			serverURL: "https://api.pugetsound.onebusaway.org",
			apiKey:    "org.onebusaway.iphone",
			useVCR:    false,
			wantErr:   true,
			errString: "does not support metrics API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *http.Client

			if tt.useVCR {
				rec, err := recorder.New(filepath.Join("testdata", "vcr", tt.cassette))
				if err != nil {
					t.Fatalf("Failed to create recorder: %v", err)
				}
				defer rec.Stop()

				client = &http.Client{
					Transport: rec,
					Timeout:   10 * time.Second,
				}
			}
			staticStore.Set(tt.slugID, staticData)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			tracker := NewUnmatchedStopTracker()
			err := fetchObaAPIMetrics(tt.slugID, tt.serverURL, tt.apiKey, client, staticStore, logger, tracker)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
					return
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestFetchObaAPIMetrics_SanitizesServerURLLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "key=SUPERSECRETOBAKEY" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Writing to ResponseWriter in tests, error can be safely ignored.
			// #nosec G104
			w.Write([]byte(`{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":0,"agencyIDs":[]}}}`))
			return
		}
		http.Error(w, "missing key", http.StatusUnauthorized)
	}))
	defer server.Close()

	// Base URL carrying userinfo credentials, to ensure they get stripped too.
	serverBaseURL := strings.Replace(server.URL, "://", "://user:pass@", 1)
	apiKey := "SUPERSECRETOBAKEY"
	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()

	if err := fetchObaAPIMetrics("42", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := make(chan prometheus.Metric, 8)
	ObaApiStatus.Collect(c)
	close(c)

	gotURL := ""
	for m := range c {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		labels := make(map[string]string)
		for _, lp := range pb.Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["agency_id"] == "42" {
			gotURL = labels["server_url"]
		}
	}

	// The caller-provided base URL carried userinfo, and the query string carries the
	// API key; the label must reduce to the clean scheme://host of the httptest server.
	wantURL := server.URL
	if gotURL != wantURL {
		t.Fatalf("expected server_url label %q, got %q", wantURL, gotURL)
	}
	if strings.Contains(gotURL, apiKey) || strings.Contains(gotURL, "key=") || strings.Contains(gotURL, "user:pass") {
		t.Fatalf("credential leaked in server_url label %q", gotURL)
	}
}

func TestFetchObaAPIMetrics_DoesNotLeakAPIKeyInLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// #nosec G104
		w.Write([]byte(`{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":0,"agencyIDs":[]}}}`))
	}))
	defer server.Close()

	apiKey := "SUPERSECRETOBAKEY"
	serverBaseURL := strings.Replace(server.URL, "://", "://user:pass@", 1)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	staticStore := gtfs.NewStaticStore()
	tracker := NewUnmatchedStopTracker()

	if err := fetchObaAPIMetrics("42", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logged := logBuf.String()
	if strings.Contains(logged, apiKey) || strings.Contains(logged, "key=") || strings.Contains(logged, "user:pass") {
		t.Fatalf("credential leaked in structured logs:\n%s", logged)
	}
}

func TestFetchObaAPIMetrics_ErrorDoesNotLeakAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	apiKey := "SUPERSECRETOBAKEY"
	serverBaseURL := strings.Replace(server.URL, "://", "://user:pass@", 1)

	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()

	err := fetchObaAPIMetrics("42", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker)
	if err == nil {
		t.Fatal("expected error but got none")
	}
	if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), "key=") || strings.Contains(err.Error(), "user:pass") {
		t.Fatalf("credential leaked in error message: %v", err)
	}
}
