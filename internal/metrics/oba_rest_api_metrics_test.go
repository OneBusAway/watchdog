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
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestFetchObaAPIMetrics(t *testing.T) {
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticData := models.NewStaticData(staticBundle)

	const successBody = `{"code":200,"currentTime":1746323809556,"data":{"entry":{"agenciesWithCoverageCount":1,"agencyIDs":["unitrans"],"realtimeRecordsTotal":{"unitrans":3},"realtimeTripCountsMatched":{"unitrans":3},"realtimeTripCountsUnmatched":{"unitrans":0},"realtimeTripIDsUnmatched":{"unitrans":[]},"scheduledTripsCount":{"unitrans":5},"stopIDsMatchedCount":{"unitrans":5},"stopIDsUnmatched":{"unitrans":[]},"stopIDsUnmatchedCount":{"unitrans":0},"timeSinceLastRealtimeUpdate":{"unitrans":24}},"references":{"agencies":[],"routes":[],"situations":[],"stopTimes":[],"stops":[],"trips":[]}},"text":"OK","version":2}`

	tests := []struct {
		name       string
		agencyID   string
		agencyName string
		serverURL  string
		apiKey     string
		useVCR     bool
		cassette   string
		response   string
		statusCode int
		wantErr    bool
		errString  string
	}{
		{
			name:       "successful request",
			agencyID:   "unitrans",
			agencyName: "Unitrans",
			serverURL:  "https://oba-api.onrender.com",
			apiKey:     "org.onebusaway.iphone",
			useVCR:     true,
			cassette:   "oba_metrics_api_successful_request",
			wantErr:    false,
		},
		{
			name:       "not found error",
			agencyID:   "invalid-region",
			agencyName: "Puget Sound",
			apiKey:     "org.onebusaway.iphone",
			statusCode: http.StatusNotFound,
			wantErr:    true,
			errString:  "does not support metrics API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *http.Client
			var baseURL string

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
				baseURL = tt.serverURL
			} else {
				server := setupObaServer(t, tt.response, tt.statusCode)
				defer server.Close()
				client = &http.Client{Timeout: 10 * time.Second}
				baseURL = server.URL
			}

			staticStore := gtfs.NewStaticStore()
			staticStore.Set(tt.agencyID, staticData)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			tracker := NewUnmatchedStopTracker()

			err := fetchObaAPIMetrics(tt.agencyID, tt.agencyName, baseURL, tt.apiKey, client, staticStore, logger, tracker)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.errString) {
					t.Errorf("expected error to contain %q, got %q", tt.errString, err.Error())
				}
				status, err := getMetricValue(ObaApiStatus, map[string]string{
					"agency_id":   tt.agencyID,
					"agency_name": tt.agencyName,
					"server_url":  baseURL + metricsEndpoint,
				})
				if err != nil {
					t.Fatalf("failed to read oba_api_status: %v", err)
				}
				if status != 0 {
					t.Fatalf("expected oba_api_status to be 0 on failure, got %v", status)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			status, err := getMetricValue(ObaApiStatus, map[string]string{
				"agency_id":   tt.agencyID,
				"agency_name": tt.agencyName,
				"server_url":  baseURL + metricsEndpoint,
			})
			if err != nil {
				t.Fatalf("failed to read oba_api_status: %v", err)
			}
			if status != 1 {
				t.Fatalf("expected oba_api_status to be 1 on success, got %v", status)
			}

			records, err := getMetricValue(ObaRealtimeRecords, map[string]string{"agency_id": tt.agencyID, "agency_name": tt.agencyName})
			if err != nil {
				t.Fatalf("failed to read records for %s: %v", tt.agencyID, err)
			}
			if records != 3 {
				t.Fatalf("expected oba_realtime_records_count to be 3, got %v", records)
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

	if err := fetchObaAPIMetrics("42", "Sanitize Server", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
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
	// API key; the label must reduce to the clean scheme://host and endpoint path
	// of the httptest server.
	wantURL := server.URL + "/api/where/metrics.json"
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

	if err := fetchObaAPIMetrics("unitrans-a", "Unitrans A", serverA.URL, "key", client, staticStore, logger, tracker); err != nil {
		t.Fatalf("server A: unexpected error: %v", err)
	}
	if err := fetchObaAPIMetrics("unitrans-b", "Unitrans B", serverB.URL, "key", client, staticStore, logger, tracker); err != nil {
		t.Fatalf("server B: unexpected error: %v", err)
	}

	recordsA, err := getMetricValue(ObaRealtimeRecords, map[string]string{"agency_id": "unitrans-a", "agency_name": "Unitrans A"})
	if err != nil {
		t.Fatalf("failed to read records for unitrans-a: %v", err)
	}
	if recordsA != 3 {
		t.Fatalf("expected oba_realtime_records_count{agency_id=\"unitrans-a\"} to be 3, got %v", recordsA)
	}

	recordsB, err := getMetricValue(ObaRealtimeRecords, map[string]string{"agency_id": "unitrans-b", "agency_name": "Unitrans B"})
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

	if err := fetchObaAPIMetrics("requested", "Requested Server", server.URL, "key", &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
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

	if err := fetchObaAPIMetrics("42", "No Leak Server", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
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

	err := fetchObaAPIMetrics("42", "Error Server", serverBaseURL, apiKey, &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker)
	if err == nil {
		t.Fatal("expected error but got none")
	}
	if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), "key=") || strings.Contains(err.Error(), "user:pass") {
		t.Fatalf("credential leaked in error message: %v", err)
	}
}

func TestFetchObaAPIMetrics_SetsStatusZeroOnFailure(t *testing.T) {
	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()

	server404 := setupObaServer(t, "", http.StatusNotFound)
	defer server404.Close()

	server500 := setupObaServer(t, `{"code":500}`, http.StatusInternalServerError)
	defer server500.Close()

	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// #nosec G104
		w.Write([]byte(`{not json`))
	}))
	defer serverBadJSON.Close()

	serverClosed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := serverClosed.URL
	serverClosed.Close()

	tests := []struct {
		name    string
		baseURL string
	}{
		{"404", server404.URL},
		{"500", server500.URL},
		{"malformed JSON", serverBadJSON.URL},
		{"connection refused", closedURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fetchObaAPIMetrics("fail-status", "Fail Status", tt.baseURL, "key", &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker)
			if err == nil {
				t.Fatal("expected error but got none")
			}

			status, err := getMetricValue(ObaApiStatus, map[string]string{
				"agency_id":   "fail-status",
				"agency_name": "Fail Status",
				"server_url":  tt.baseURL + metricsEndpoint,
			})
			if err != nil {
				t.Fatalf("failed to read oba_api_status: %v", err)
			}
			if status != 0 {
				t.Fatalf("expected oba_api_status to be 0 for %s, got %v", tt.name, status)
			}
		})
	}
}

func TestFetchObaAPIMetrics_StatusResetsFromOneToZero(t *testing.T) {
	// A metrics-endpoint outage must flip the previously-successful series back
	// to 0; it must not stay 1 forever after the first success.
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// #nosec G104
			w.Write([]byte(`{"code":200,"text":"OK","version":2,"currentTime":123,"data":{"entry":{"agenciesWithCoverageCount":0,"agencyIDs":["status-reset"]}}}`))
			return
		}
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()

	statusLabels := map[string]string{
		"agency_id":   "status-reset",
		"agency_name": "Status Reset",
		"server_url":  server.URL + metricsEndpoint,
	}

	if err := fetchObaAPIMetrics("status-reset", "Status Reset", server.URL, "key", &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	status, err := getMetricValue(ObaApiStatus, statusLabels)
	if err != nil {
		t.Fatalf("failed to read oba_api_status after success: %v", err)
	}
	if status != 1 {
		t.Fatalf("expected oba_api_status to be 1 after success, got %v", status)
	}

	if err := fetchObaAPIMetrics("status-reset", "Status Reset", server.URL, "key", &http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker); err == nil {
		t.Fatal("expected error on second call")
	}
	status, err = getMetricValue(ObaApiStatus, statusLabels)
	if err != nil {
		t.Fatalf("failed to read oba_api_status after failure: %v", err)
	}
	if status != 0 {
		t.Fatalf("expected oba_api_status to reset to 0 after failure, got %v", status)
	}
}

func TestFetchObaAPIMetricsNilClient(t *testing.T) {
	staticStore := gtfs.NewStaticStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := NewUnmatchedStopTracker()

	err := fetchObaAPIMetrics("nil-client", "Nil Client", "http://example.com", "key", nil, staticStore, logger, tracker)
	if err == nil {
		t.Fatal("expected error when passing nil http client, got none")
	}
}
