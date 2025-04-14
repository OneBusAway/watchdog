package metrics

import (
	"net/http"
	"testing"
)

func TestScheduleTripForRoute(t *testing.T) {
	t.Run("Success", func(t *testing.T) {

		ts := setupObaServer(t, `{
            "code": 200,
            "currentTime": 1234567890000,
            "text": "OK",
            "version": 2,
            "data": {
                "entry": {
                    "routeId": "100",
                    "trips": [
                        {"id": "trip1"},
                        {"id": "trip2"},
                        {"id": "trip3"}
                    ]
                }
            }
        }`, http.StatusOK)
		defer ts.Close()

		// Create server with mock URL
		server := createTestServer(ts.URL, "Test Server", 999, "test-key", "", "", "", "")

		// Call the function that updates the metric
		count, err := GetScheduledTripRoute(server, "100")
		if err != nil {
			t.Errorf("GetScheduledTripRoute failed: %v", err)
		}

		// Check the returned count
		if count != 3 {
			t.Errorf("Expected trip count to be 3, got %v", count)
		}

		// Check the metric value
		labels := map[string]string{
			"server_id": "999",
			"route_id":  "100",
		}
		scheduledTripForRoute, err := getMetricValue(ScheduleTripForRoute, labels)
		if err != nil {
			t.Errorf("Failed to get ScheduleTripForRoute metric value: %v", err)
		}

		if scheduledTripForRoute != 3 {
			t.Errorf("Expected ScheduleTripForRoute metric to be 3, got %v", scheduledTripForRoute)
		}
	})

	t.Run("Error", func(t *testing.T) {
		ts := setupObaServer(t, `{
            "code": 200,
            "currentTime": 1234567890000,
            "text": "OK",
            "version": 2,
            "data": {
                "entry": {
                    "routeId": "100",
                    "trips": [
                        {"id": "trip1"},
                        {"id": "trip2"},
                        {"id": "trip3"}
                    ]
                }
            }
        }`, http.StatusInternalServerError)
		defer ts.Close()

		server := createTestServer(ts.URL, "Test Server", 999, "test-key", "", "", "", "")

		_, err := GetScheduledTripRoute(server, "100")

		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
	})

	t.Run("nil Response", func(t *testing.T) {
		ts := setupObaServer(t, `{
            "code": 200,
            "currentTime": 1234567890000,
            "text": "OK",
            "version": 2,
            "data": {}
        }`, http.StatusOK)
		defer ts.Close()

		server := createTestServer(ts.URL, "Test Server", 999, "test-key", "", "", "", "")

		count, err := GetScheduledTripRoute(server, "100")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if count != 0 {
			t.Fatalf("Expected count to be 0, got %d", count)
		}
	})
}
