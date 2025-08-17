package gtfs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	remoteGtfs "github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestDownloadGTFSBundles(t *testing.T) {
	servers := []models.ObaServer{
		{ID: 1, GtfsUrl: "https://example.com/gtfs.zip"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	boundingBoxStore := geo.NewBoundingBoxStore()
	staticStore := NewStaticStore()
	downloadGTFSBundles(servers, logger, boundingBoxStore, staticStore)

}

func TestRefreshGTFSBundles(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	servers := []models.ObaServer{{ID: 1, Name: "Test Server", GtfsUrl: "http://example.com/gtfs.zip"}}
	boundingBoxStore := geo.NewBoundingBoxStore()
	staticStore := NewStaticStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go refreshGTFSBundles(ctx, servers, logger, 10*time.Millisecond, boundingBoxStore, staticStore)

	time.Sleep(15 * time.Millisecond)

	t.Log("refreshGTFSBundles executed without crashing")
}

func TestDownloadAndStoreGTFSBundle(t *testing.T) {
	mockServer := setupGtfsServer(t, "gtfs.zip")
	serverID := 1
	staticStore := NewStaticStore()
	t.Run("Success Response", func(t *testing.T) {
		err := downloadAndStoreGTFSBundle(mockServer.URL, serverID, staticStore)
		if err != nil {
			t.Fatalf("DownloadGTFSBundle failed: %v", err)
		}

		data := readFixture(t, "gtfs.zip")
		expectedStaticData, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
		if err != nil {
			t.Fatalf("failed to parse expected GTFS static data from fixture: %v", err)
		}
		if expectedStaticData == nil {
			t.Fatal("parsed expected static data is nil; expected valid GTFS data")
		}
		if expectedStaticData.Agencies == nil {
			t.Fatal("expected static data has nil Agencies slice; expected it to be parsed")
		}

		staticData, exists := staticStore.Get(serverID)
		if !exists {
			t.Fatalf("expected static data for server ID %d, but it was not found in the store", serverID)
		}
		if staticData == nil {
			t.Fatal("static data retrieved from the store is nil; expected non-nil value")
		}

		// For simplicity, we validate the content of agency.txt by comparing the agency IDs.
		// We assume that if the agency IDs match, the GTFS static data was parsed and stored correctly.
		// This level of verification is sufficient for this test.
		//
		// Note: We rely on agency.txt as it is a required GTFS file.
		// Make sure the test data provided includes a non-empty agency.txt file.

		if len(expectedStaticData.Agencies) != len(staticData.Agencies) {
			t.Fatalf("expected %d agencies, got %d", len(expectedStaticData.Agencies), len(staticData.Agencies))
		}
		if len(expectedStaticData.Agencies) == 0 {
			t.Fatal("expected Agencies slice is empty; can't verify content consistency")
		}
		expectedAgencyIDs := make(map[string]struct{})
		for _, agency := range expectedStaticData.Agencies {
			expectedAgencyIDs[agency.Id] = struct{}{}
		}
		if staticData.Agencies == nil {
			t.Fatal("stored static data has nil Agencies slice; expected it to be populated")
		}
		if len(staticData.Agencies) == 0 {
			t.Fatal("stored Agencies slice is empty; static data likely not parsed correctly")
		}
		for _, agency := range staticData.Agencies {
			if _, ok := expectedAgencyIDs[agency.Id]; !ok {
				t.Fatalf("unexpected agency ID %s found in stored static data", agency.Id)
			}
		}
	})

	t.Run("Invalid URL", func(t *testing.T) {
		invalidURL := "http://invalid-url"
		err := downloadAndStoreGTFSBundle(invalidURL, 2, staticStore)
		if err == nil {
			t.Errorf("Expected error for invalid URL, got none")
		}
	})

}

func TestGetStopLocationsByIDs(t *testing.T) {
	server := models.ObaServer{ID: 1, Name: "test"}

	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticData := models.NewStaticData(staticBundle)
	staticStore := NewStaticStore()
	staticStore.Set(server.ID, staticData)

	t.Run("Valid stop IDs", func(t *testing.T) {
		stopIDs := []string{"11060", "1108"} // Make sure these exist in your test GTFS
		stops, err := getStopLocationsByIDs(server.ID, stopIDs, staticStore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) == 0 {
			t.Fatalf("expected some matched stops, got 0")
		}
	})

	t.Run("Invalid stop IDs", func(t *testing.T) {
		stopIDs := []string{"nonexistent1", "nonexistent2"}
		stops, err := getStopLocationsByIDs(server.ID, stopIDs, staticStore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) != 0 {
			t.Errorf("expected 0 matched stops, got %d", len(stops))
		}
	})
}

func TestParseGTFSFromCache(t *testing.T) {
	server := models.ObaServer{ID: 1, Name: "Test"}

	tests := []struct {
		name      string
		cachePath string
		expectErr bool
	}{
		{
			name:      "Valid GTFS file",
			cachePath: "../../testdata/gtfs.zip",
			expectErr: false,
		},
		{
			name:      "Invalid path",
			cachePath: "../../testdata/does-not-exist.zip",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			staticData, err := ParseGTFSFromCache(tc.cachePath, server.ID)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if staticData == nil {
				t.Fatal("expected staticData to be non-nil")
			}
			if len(staticData.Stops) == 0 {
				t.Error("expected at least one stop in GTFS data, got 0")
			}
		})
	}
}

func TestFetchAndStoreGTFSRTFeed(t *testing.T) {
	t.Run("Success Case", func(t *testing.T) {
		mockServer := setupGtfsServer(t, "gtfs_rt_feed_vehicles.pb")
		defer mockServer.Close()

		server := models.ObaServer{
			ID:                 1,
			VehiclePositionUrl: mockServer.URL,
			GtfsRtApiKey:       "X-Test-Header",
			GtfsRtApiValue:     "test-value",
		}

		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if realtimeStore.Get() == nil {
			t.Fatalf("Expected realtimeStore to contain parsed GTFS-RT data, but it is nil")
		}

		data := readFixture(t, "gtfs_rt_feed_vehicles.pb")
		gtfsRT, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
		if err != nil {
			t.Fatalf("Failed to parse GTFS-RT data: %v", err)
		}
		expectedRtData := models.NewRealtimeData(gtfsRT)
		realtimeData := realtimeStore.Get()
		if realtimeData == nil {
			t.Fatal("realtimeData is nil; expected non-nil GTFS-RT data")
		}

		if len(expectedRtData.Vehicles) == 0 {
			t.Fatalf("Make sure that data contains at least one vehicle in the GTFS-RT feed in testdata/gtfs_rt_feed_vehicles.pb")
		}

		if len(realtimeData.Vehicles) != len(expectedRtData.Vehicles) {
			t.Fatalf("Expected %d vehicles, got %d", len(expectedRtData.Vehicles), len(realtimeData.Vehicles))
		}

		expectedMap := make(map[string]struct{})
		for _, vehicle := range expectedRtData.Vehicles {
			if vehicle.ID != nil {
				expectedMap[vehicle.ID.ID] = struct{}{}
			}
		}
		countExpectedNilIDs := len(expectedRtData.Vehicles) - len(expectedMap)
		countNilIDs := 0
		for _, vehicle := range realtimeData.Vehicles {
			if vehicle.ID != nil {
				if _, exists := expectedMap[vehicle.ID.ID]; !exists {
					t.Errorf("Unexpected vehicle ID %s found in GTFS-RT data", vehicle.ID.ID)
				}
			} else {
				countNilIDs++
			}
		}
		if countNilIDs != countExpectedNilIDs {
			t.Errorf("Expected %d vehicles with nil IDs, got %d", countExpectedNilIDs, countNilIDs)
		}
	})

	t.Run("Failure Case - Invalid URL", func(t *testing.T) {
		server := models.ObaServer{
			ID:                 2,
			VehiclePositionUrl: "://invalid-url",
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()

		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
		if err == nil {
			t.Error("Expected error due to invalid URL, got nil")
		}
	})

	t.Run("Failure Case - Closed Server", func(t *testing.T) {
		mockServer := httptest.NewServer(nil)
		mockServer.Close()

		server := models.ObaServer{
			ID:                 3,
			VehiclePositionUrl: mockServer.URL,
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
		if err == nil {
			t.Error("Expected error when accessing closed server, got nil")
		}
	})
}
