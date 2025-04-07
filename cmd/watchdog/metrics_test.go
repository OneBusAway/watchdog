package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
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

func TestCheckServer(t *testing.T) {
	ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"entry":{"readableTime":"Test Time"}}}`, http.StatusOK)
	defer ts.Close()

	testServer := createTestServer(ts.URL, "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

	metrics.ServerPing(testServer)
	time.Sleep(100 * time.Millisecond)

	metricValue, err := getMetricValue(metrics.ObaApiStatus, map[string]string{
		"server_id":  "999",
		"server_url": testServer.ObaBaseURL,
	})
	if err != nil {
		t.Fatal(err)
	}

	if metricValue != 1 {
		t.Errorf("Expected metric value to be 1 (working), got %v", metricValue)
	}
}

func TestCheckBundleExpiration(t *testing.T) {
	fixturePath := getFixturePath(t, "gtfs.zip")
	fixedTime := time.Date(2025, 1, 12, 20, 16, 38, 0, time.UTC)

	testServer := createTestServer("www.example.com", "Test Server", 999, "", "www.example.com", "test-api-value", "test-api-key", "1")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	earliest, latest, err := metrics.CheckBundleExpiration(fixturePath, logger, fixedTime, testServer)
	if err != nil {
		t.Fatalf("CheckBundleExpiration failed: %v", err)
	}

	// This is the current gtfs.zip file in the testdata directory expected earliest and latest expiration days
	expectedEarliest := int(time.Date(2024, 11, 22, 0, 0, 0, 0, time.UTC).Sub(fixedTime).Hours() / 24)
	expectedLatest := int(time.Date(2025, 3, 28, 0, 0, 0, 0, time.UTC).Sub(fixedTime).Hours() / 24)

	if earliest != expectedEarliest {
		t.Errorf("Expected earliest expiration days to be %d, got %d", expectedEarliest, earliest)
	}
	if latest != expectedLatest {
		t.Errorf("Expected latest expiration days to be %d, got %d", expectedLatest, latest)
	}

	earliestMetric, err := getMetricValue(metrics.BundleEarliestExpirationGauge, map[string]string{"server_id": "999"})
	if err != nil {
		t.Errorf("Failed to get earliest expiration metric value: %v", err)
	}
	if earliestMetric != float64(expectedEarliest) {
		t.Errorf("Expected earliest expiration metric to be %v, got %v", expectedEarliest, earliestMetric)
	}

	latestMetric, err := getMetricValue(metrics.BundleLatestExpirationGauge, map[string]string{"server_id": "999"})
	if err != nil {
		t.Errorf("Failed to get latest expiration metric value: %v", err)
	}
	if latestMetric != float64(expectedLatest) {
		t.Errorf("Expected latest expiration metric to be %v, got %v", expectedLatest, latestMetric)
	}
}

func TestCheckAgenciesWithCoverage(t *testing.T) {
	// Test case: Successful execution

	t.Run("Success", func(t *testing.T) {
		fixturePath := getFixturePath(t, "gtfs.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

		ts := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer ts.Close()

		testServer := createTestServer(ts.URL, "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := metrics.CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer)
		if err != nil {
			t.Fatalf("CheckAgenciesWithCoverageMatch failed: %v", err)
		}

		agencyMatchMetric, err := getMetricValue(metrics.AgenciesMatch, map[string]string{"server_id": "999"})
		if err != nil {
			t.Errorf("Failed to get AgenciesMatch metric value: %v", err)
		}

		if agencyMatchMetric != 1 {
			t.Errorf("Expected agency match metric to be 1, got %v", agencyMatchMetric)
		}
	})

	// Test case: Error opening file
	t.Run("ErrorOpeningFile", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := metrics.CheckAgenciesWithCoverageMatch("invalid/path/to/gtfs.zip", logger, testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	// Test case: Error reading file
	t.Run("ErrorReadingFile", func(t *testing.T) {
		fixturePath := getFixturePath(t, "empty.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := metrics.CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	// Test case: Error parsing GTFS data
	t.Run("ErrorParsingGTFSData", func(t *testing.T) {
		fixturePath := getFixturePath(t, "invalid_gtfs.zip")
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", "http://example.com", "test-api-value", "test-api-key", "1")

		err := metrics.CheckAgenciesWithCoverageMatch(fixturePath, logger, testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})
}

func TestCheckVehicleCountMatch(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		gtfsRtServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")

		defer gtfsRtServer.Close()

		obaServer := setupObaServer(t, `{"code":200,"currentTime":1234567890000,"text":"OK","version":2,"data":{"list":[{"agencyId":"1"}]}}`, http.StatusOK)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := metrics.CheckVehicleCountMatch(testServer)
		if err != nil {
			t.Fatalf("CheckVehicleCountMatch failed: %v", err)
		}

		realtimeData, err := gtfs.ParseRealtime(readFixture(t, "gtfs_rt_feed_vehicles.pb"), &gtfs.ParseRealtimeOptions{})
		if err != nil {
			t.Fatalf("Failed to parse GTFS-RT fixture data: %v", err)
		}

		t.Log("Number of vehicles in GTFS-RT feed:", len(realtimeData.Vehicles))
	})

	t.Run("GTFS-RT Error", func(t *testing.T) {
		// Set up a GTFS-RT server that returns an error
		gtfsRtServer := setupTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer gtfsRtServer.Close()

		testServer := createTestServer("http://example.com", "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := metrics.CheckVehicleCountMatch(testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})

	t.Run("OBA API Error", func(t *testing.T) {
		gtfsRtServer := setupGtfsRtServer(t, "gtfs_rt_feed_vehicles.pb")
		defer gtfsRtServer.Close()

		obaServer := setupObaServer(t, `{}`, http.StatusInternalServerError)
		defer obaServer.Close()

		testServer := createTestServer(obaServer.URL, "Test Server", 999, "test-key", gtfsRtServer.URL, "test-api-value", "test-api-key", "1")

		err := metrics.CheckVehicleCountMatch(testServer)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
		t.Log("Received expected error:", err)
	})
}

// OBASdk tests
func TestGetAgenciesWithCoverage(t *testing.T) {
    t.Run("NilResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{}`, http.StatusOK)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
        }

        count, err := metrics.GetAgenciesWithCoverage(server)
        if err != nil {
            t.Fatalf("Expected no error, got %v", err)
        }

        if count != 0 {
            t.Fatalf("Expected count to be 0, got %d", count)
        }
    })

    t.Run("SuccessfulResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{"data": {"list": [{"agencyId": "1"}, {"agencyId": "2"}]}}`, http.StatusOK)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
        }

        count, err := metrics.GetAgenciesWithCoverage(server)
        if err != nil {
            t.Fatalf("Expected no error, got %v", err)
        }

        if count != 2 {
            t.Fatalf("Expected count to be 2, got %d", count)
        }
    })

    t.Run("ErrorResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
        }

        _, err := metrics.GetAgenciesWithCoverage(server)
        if err == nil {
            t.Fatal("Expected an error but got nil")
        }
    })
}


func TestVehiclesForAgencyAPI(t *testing.T) {
    t.Run("NilResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{"data": {"list": []}}`, http.StatusOK)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
            AgencyID:   "test-agency",
        }

        count, err := metrics.VehiclesForAgencyAPI(server)
        if err != nil {
            t.Fatalf("Expected no error, got %v", err)
        }

        if count != 0 {
            t.Fatalf("Expected count to be 0, got %d", count)
        }
    })

    t.Run("SuccessfulResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{"data": {"list": [{"vehicleId": "1"}, {"vehicleId": "2"}]}}`, http.StatusOK)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
            AgencyID:   "test-agency",
        }

        count, err := metrics.VehiclesForAgencyAPI(server)
        if err != nil {
            t.Fatalf("Expected no error, got %v", err)
        }

        if count != 2 {
            t.Fatalf("Expected count to be 2, got %d", count)
        }
    })

    t.Run("ErrorResponse", func(t *testing.T) {
        ts := setupObaServer(t, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
        defer ts.Close()

        server := models.ObaServer{
            Name:       "Test Server",
            ID:         999,
            ObaBaseURL: ts.URL,
            ObaApiKey:  "test-key",
            AgencyID:   "test-agency",
        }

        _, err := metrics.VehiclesForAgencyAPI(server)
        if err == nil {
            t.Fatal("Expected an error but got nil")
        }
    })
}
