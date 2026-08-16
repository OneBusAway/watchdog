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
			w.Write([]byte(`{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":0,"agencyIDs":["42"]}}}`))
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

func TestFetchObaAPIMetrics_LabelsWithConfiguredAgencyID(t *testing.T) {
	// Each server's per-agency statistics are keyed by the agency ID Watchdog is
	// configured to monitor, and the server lists those IDs in its agencyIDs
	// metadata. The resulting series are labeled with the configured agency ID and
	// the two servers cannot overwrite each other's metrics.
	serverA := setupObaServer(t, `{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":1,"agencyIDs":["unitrans-a"],"realtimeRecordsTotal":{"unitrans-a":3}}}}`, http.StatusOK)
	defer serverA.Close()
	serverB := setupObaServer(t, `{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":1,"agencyIDs":["unitrans-b"],"realtimeRecordsTotal":{"unitrans-b":5}}}}`, http.StatusOK)
	defer serverB.Close()

	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()
	client := &http.Client{Timeout: 10 * time.Second}

	if err := fetchObaAPIMetrics("unitrans-a", serverA.URL, "key", client, staticStore, logger, tracker); err != nil {
		t.Fatalf("server A: unexpected error: %v", err)
	}
	if err := fetchObaAPIMetrics("unitrans-b", serverB.URL, "key", client, staticStore, logger, tracker); err != nil {
		t.Fatalf("server B: unexpected error: %v", err)
	}

	recordsA, err := getMetricValue(ObaRealtimeRecords, map[string]string{"agency_id": "unitrans-a"})
	if err != nil {
		t.Fatalf("failed to read records for unitrans-a: %v", err)
	}
	if recordsA != 3 {
		t.Fatalf("expected oba_realtime_records_count{agency_id=\"unitrans-a\"} to be 3, got %v", recordsA)
	}

	recordsB, err := getMetricValue(ObaRealtimeRecords, map[string]string{"agency_id": "unitrans-b"})
	if err != nil {
		t.Fatalf("failed to read records for unitrans-b: %v", err)
	}
	if recordsB != 5 {
		t.Fatalf("expected oba_realtime_records_count{agency_id=\"unitrans-b\"} to be 5, got %v", recordsB)
	}
}

func TestFetchObaAPIMetrics_AgencyNotListedInResponse(t *testing.T) {
	// The server reports per-agency stats keyed by the requested agency, but that
	// agency is absent from the agencyIDs metadata. fetchObaAPIMetrics must report
	// the mismatch and set none of the per-agency metrics for it.
	server := setupObaServer(t, `{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":1,"agencyIDs":["other"],"realtimeRecordsTotal":{"requested":5},"realtimeTripCountsMatched":{"requested":3},"realtimeTripCountsUnmatched":{"requested":1},"scheduledTripsCount":{"requested":4},"stopIDsMatchedCount":{"requested":2},"stopIDsUnmatchedCount":{"requested":1},"timeSinceLastRealtimeUpdate":{"requested":10},"stopIDsUnmatched":{"requested":["stop-1"]}}}}`, http.StatusOK)
	defer server.Close()

	staticStore := gtfs.NewStaticStore()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	tracker := NewUnmatchedStopTracker()

	if err := fetchObaAPIMetrics("requested", server.URL, "key", &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(logBuf.String(), "Configured agency not found in OBA metrics response") {
		t.Fatalf("expected error log about missing agency, got:\n%s", logBuf.String())
	}

	c := make(chan prometheus.Metric, 8)
	ObaRealtimeRecords.Collect(c)
	close(c)
	for m := range c {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		for _, lp := range pb.Label {
			if lp.GetName() == "agency_id" && lp.GetValue() == "requested" {
				t.Fatalf("expected no oba_realtime_records series for agency %q, got one", "requested")
			}
		}
	}
}

func TestFetchObaAPIMetrics_DoesNotLeakAPIKeyInLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// #nosec G104
		w.Write([]byte(`{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":0,"agencyIDs":["42"]}}}`))
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
