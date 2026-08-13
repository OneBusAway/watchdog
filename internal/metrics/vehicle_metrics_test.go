package metrics

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

var realtimeStore *gtfs.RealtimeStore

func TestMain(m *testing.M) {
	realtimeStore = gtfs.NewRealtimeStore()

	absPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "gtfs_rt_feed_vehicles.pb"))
	if err != nil {
		fmt.Printf("Failed to get absolute path: %v\n", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		fmt.Printf("Failed to read GTFS-RT fixture: %v\n", err)
		os.Exit(1)
	}

	gtfsRT, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		fmt.Printf("Failed to parse GTFS-RT data: %v\n", err)
		os.Exit(1)
	}
	realtimeData := models.NewRealtimeData(gtfsRT)
	realtimeStore.Set(realtimeData)

	exitCode := m.Run()
	os.Exit(exitCode)
}

func TestCountVehiclePositions(t *testing.T) {
	t.Run("Valid GTFS-RT response", func(t *testing.T) {

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: "Value of VehiclePositionUrl",
		}
		count, err := countVehiclePositions(server, realtimeStore)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if count < 0 {
			t.Fatalf("Expected non-negative count, got %d", count)
		}
	})

	t.Run("gtfs_rt_url label is sanitized", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 77,
			VehiclePositionUrl: "https://rt.example.com/vehiclePositions.pb?api_key=SUPERSECRET&token=TOPSECRET",
		}
		if _, err := countVehiclePositions(server, realtimeStore); err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		c := make(chan prometheus.Metric, 1)
		RealtimeVehiclePositions.With(prometheus.Labels{
			"gtfs_rt_url": "https://rt.example.com",
			"server_id":   "77",
		}).Collect(c)
		m := <-c

		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("Failed to write metric: %v", err)
		}

		labels := make(map[string]string)
		for _, lp := range pb.Label {
			labels[lp.GetName()] = lp.GetValue()
		}

		for name, value := range labels {
			if strings.Contains(value, "SUPERSECRET") || strings.Contains(value, "TOPSECRET") || strings.Contains(value, "api_key") {
				t.Fatalf("credential leaked in label %s=%q", name, value)
			}
			if name == "gtfs_rt_url" && value != "https://rt.example.com" {
				t.Fatalf("expected sanitized gtfs_rt_url, got %q", value)
			}
		}
	})
}

func TestCountActiveVehiclesForAgency(t *testing.T) {
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

		count, err := countActiveVehiclesForAgency(server)
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

		count, err := countActiveVehiclesForAgency(server)
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

		_, err := countActiveVehiclesForAgency(server)
		if err == nil {
			t.Fatal("Expected an error but got nil")
		}
	})
}

func TestTrackInvalidVehiclesAndStoppedOutOfBounds(t *testing.T) {
	boundingBoxStore := geo.NewBoundingBoxStore()
	boundingBoxStore.Set(1, geo.BoundingBox{
		MinLat: -90, MaxLat: 90,
		MinLon: -180, MaxLon: 180,
	})

	t.Run("Success with valid vehicle positions", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: "Value of VehiclePositionUrl",
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := trackInvalidVehiclesAndStoppedOutOfBounds(server, boundingBoxStore, realtimeStore)
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("Failure due to missing bounding box", func(t *testing.T) {

		server := models.ObaServer{
			ID:                 99, // no bounding box for this ID
			VehiclePositionUrl: "Value of VehiclePositionUrl",
			GtfsRtApiKey:       "Authorization",
			GtfsRtApiValue:     "test-key",
		}

		err := trackInvalidVehiclesAndStoppedOutOfBounds(server, boundingBoxStore, realtimeStore)
		if err == nil {
			t.Error("Expected error due to missing bounding box, got nil")
		}
	})
}
