package gtfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

func TestDownloadGTFSBundles(t *testing.T) {
	servers := []models.ObaServer{
		{AgencyID: "agency-1", GtfsStaticFeeds: []string{"https://example.com/gtfs.zip"}},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	boundingBoxStore := geo.NewBoundingBoxStore()
	staticStore := NewStaticStore()
	ctx := context.Background()
	client := &http.Client{Timeout: 10 * time.Second}
	downloadGTFSBundles(ctx, client, servers, logger, boundingBoxStore, staticStore, NewRouteAgencyIndex(), nil, nil, 1)

}

func TestIncompleteStaticRefreshPreservesPriorCompleteSnapshot(t *testing.T) {
	bundle := readFixture(t, "gtfs.zip")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good.zip" {
			_, _ = w.Write(bundle)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	server := models.ObaServer{
		ServerName: "OBA", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL,
		GtfsStaticFeeds: []string{ts.URL + "/good.zip", ts.URL + "/failed.zip"},
	}
	store := NewStaticStore()
	previous := &models.StaticData{}
	previousFetch := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	store.Set(server.ServerKey(), previous)
	store.SetFetchTime(server.ServerKey(), previousFetch)

	results := downloadGTFSBundles(context.Background(), ts.Client(), []models.ObaServer{server},
		slog.New(slog.NewTextHandler(io.Discard, nil)), geo.NewBoundingBoxStore(), store,
		NewRouteAgencyIndex(), nil, nil, 1)

	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("expected one failed refresh result, got %+v", results)
	}
	got, ok := store.Get(server.ServerKey())
	if !ok || got != previous {
		t.Fatal("incomplete refresh replaced the previous complete snapshot")
	}
	gotFetch, ok := store.GetFetchTime(server.ServerKey())
	if !ok || !gotFetch.Equal(previousFetch) {
		t.Fatalf("fetch time changed after incomplete refresh: %v", gotFetch)
	}
}

func TestDownloadGTFSBundle(t *testing.T) {
	mockServer := setupGtfsServer(t, "gtfs.zip")
	agencyID := "agency-1"
	ctx := context.Background()
	client := &http.Client{Timeout: 10 * time.Second}
	t.Run("Success Response", func(t *testing.T) {
		staticBundle, err := downloadGTFSBundle(ctx, client, mockServer.URL, agencyID, 1)
		if err != nil {
			t.Fatalf("DownloadGTFSBundle failed: %v", err)
		}
		if staticBundle == nil {
			t.Fatal("static data retrieved from the store is nil; expected non-nil value")
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

		// For simplicity, we validate the content of agency.txt by comparing the agency IDs.
		// We assume that if the agency IDs match, the GTFS static data was parsed and stored correctly.
		// This level of verification is sufficient for this test.
		//
		// Note: We rely on agency.txt as it is a required GTFS file.
		// Make sure the test data provided includes a non-empty agency.txt file.

		if len(expectedStaticData.Agencies) != len(staticBundle.Agencies) {
			t.Fatalf("expected %d agencies, got %d", len(expectedStaticData.Agencies), len(staticBundle.Agencies))
		}
		if len(expectedStaticData.Agencies) == 0 {
			t.Fatal("expected Agencies slice is empty; can't verify content consistency")
		}
		expectedAgencyIDs := make(map[string]struct{})
		for _, agency := range expectedStaticData.Agencies {
			expectedAgencyIDs[agency.Id] = struct{}{}
		}
		if staticBundle.Agencies == nil {
			t.Fatal("stored static data has nil Agencies slice; expected it to be populated")
		}
		if len(staticBundle.Agencies) == 0 {
			t.Fatal("stored Agencies slice is empty; static data likely not parsed correctly")
		}
		for _, agency := range staticBundle.Agencies {
			if _, ok := expectedAgencyIDs[agency.Id]; !ok {
				t.Fatalf("unexpected agency ID %s found in stored static data", agency.Id)
			}
		}
	})

	t.Run("Invalid URL", func(t *testing.T) {
		invalidURL := "http://invalid-url"
		_, err := downloadGTFSBundle(ctx, client, invalidURL, "agency-2", 1)
		if err == nil {
			t.Errorf("Expected error for invalid URL, got none")
		}
	})

}

func TestAgencyParsing(t *testing.T) {
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatalf("failed to parse expected GTFS static data from fixture: %v", err)
	}

	expectedStaticAgencies := []remoteGtfs.Agency{
		{
			Id:       "40",
			Name:     "Sound Transit",
			Url:      "https://www.soundtransit.org",
			Timezone: "America/Los_Angeles",
			Language: "en",
			Phone:    "1-888-889-6368",
			FareUrl:  "https://www.soundtransit.org/ride-with-us/how-to-pay/fares",
			Email:    "main@soundtransit.org",
		},
	}

	if staticBundle.Agencies == nil {
		t.Fatal("stored static data has nil Agencies slice; expected it to be populated")
	}
	if len(staticBundle.Agencies) == 0 {
		t.Fatal("stored Agencies slice is empty; static data likely not parsed correctly")
	}

	if len(expectedStaticAgencies) != len(staticBundle.Agencies) {
		t.Fatalf("expected %d agencies, got %d", len(expectedStaticAgencies), len(staticBundle.Agencies))
	}

	expectedAgencies := make(map[string]remoteGtfs.Agency)

	for _, agency := range expectedStaticAgencies {
		expectedAgencies[agency.Id] = agency
	}

	for _, agency := range staticBundle.Agencies {
		expectedAgency, ok := expectedAgencies[agency.Id]
		if !ok {
			t.Fatalf("unexpected agency ID %s", agency.Id)
		}

		if expectedAgency != agency {
			t.Errorf(
				"agency mismatch for ID %s expected: %+v got %+v",
				agency.Id,
				expectedAgency,
				agency,
			)
		}
	}
}

func TestStopsParsing(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-1", AgencyName: "test", ObaBaseURL: "https://test.example.com"}

	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticData := models.NewStaticData(staticBundle)
	staticStore := NewStaticStore()
	staticStore.Set(server.ServerKey(), staticData)
	stopIDs := []string{"11060", "1108"} // Make sure these exist in your test GTFS
	stopsData := map[string]struct {
		stopName string
		lat      float64
		long     float64
	}{
		"11060": {stopName: "Broadway & E Denny Way", lat: 47.618425, long: -122.320940},
		"1108":  {stopName: "Westlake", lat: 47.611450, long: -122.337532},
	}

	stops, err := getStopLocationsByIDs(server.ServerKey(), stopIDs, staticStore)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stops) == 0 {
		t.Fatalf("expected some matched stops, got 0")
	}

	for _, stop := range stops {
		expected, ok := stopsData[stop.Id]
		if !ok {
			t.Fatalf("unexpected stop ID returned: %s", stop.Id)
		}
		if stop.Latitude == nil || stop.Longitude == nil {
			t.Fatalf("stop %s missing coordinates", stop.Id)
		}

		if stop.Name != expected.stopName {
			t.Errorf("stop %s name mismatch: expected %s, got %s",
				stop.Id, expected.stopName, stop.Name)
		}

		const epsilon = 1e-5
		if diff := *stop.Latitude - expected.lat; diff > epsilon || diff < -epsilon {
			t.Errorf("stop %s latitude mismatch: expected %f, got %f",
				stop.Id, expected.lat, *stop.Latitude)
		}
		if diff := *stop.Longitude - expected.long; diff > epsilon || diff < -epsilon {
			t.Errorf("stop %s longitude mismatch: expected %f, got %f",
				stop.Id, expected.long, *stop.Longitude)
		}
	}
}

func TestStoreGTFSBundleRecordsFetchTime(t *testing.T) {
	server := models.ObaServer{ServerName: "test", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: "https://test.example.com"}
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := parseStaticBundleData(data, "https://test.example.com/gtfs.zip", server.AgencyID)
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticStore := NewStaticStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	routeAgencyIndex := NewRouteAgencyIndex()

	err = storeStaticForServer(server, staticBundle, staticStore, boundingBoxStore, routeAgencyIndex, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Agency-mode stores the compact snapshot under the configured agency_id.
	storeKey := server.ServerKey()
	fetchTime, ok := staticStore.GetFetchTime(storeKey)
	if !ok {
		t.Fatalf("expected a fetch time to be recorded for server key %s", storeKey)
	}
	if time.Since(fetchTime) > time.Minute {
		t.Fatalf("fetch time should be recent, got %s", fetchTime)
	}
	if _, ok := staticStore.Get(storeKey); !ok {
		t.Fatalf("expected the bundle to be stored under the configured server key %s", storeKey)
	}
	if _, ok := boundingBoxStore.Get(storeKey); !ok {
		t.Fatalf("expected a bounding box under the configured server key %s", storeKey)
	}
}

func TestStoreStaticForServerExcludesForeignAgencyData(t *testing.T) {
	data := readFixture(t, "gtfs.zip")
	bundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	server := models.ObaServer{ServerName: "mismatch", AgencyID: "not-in-feed", ObaBaseURL: "https://mismatch.example.com"}
	staticStore := NewStaticStore()
	bounds := geo.NewBoundingBoxStore()
	key := server.ServerKey()
	bounds.Set(key, geo.BoundingBox{MinLat: 1, MaxLat: 2, MinLon: 3, MaxLon: 4})
	index := NewRouteAgencyIndex()

	if err := storeStaticForServer(server, bundle, staticStore, bounds, index, nil, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("store: %v", err)
	}
	stored, ok := staticStore.Get(key)
	if !ok || stored == nil {
		t.Fatal("expected an empty scoped snapshot to replace the old state")
	}
	if len(stored.Routes) != 0 || len(stored.Services) != 0 || len(stored.Stops) != 0 {
		t.Fatalf("foreign static data leaked into scoped snapshot: %+v", stored)
	}
	if _, ok := bounds.Get(key); ok {
		t.Fatal("expected stale agency bounding box to be deleted")
	}
	if !index.Has(key) {
		t.Fatal("expected empty attribution snapshot to be published")
	}
}

// In server-mode (no configured agency_id) agency.txt is the sole source of
// agency identity, so the bundle is keyed by every agency it declares.
func TestStoreStaticForServerModeKeysByDeclaredAgency(t *testing.T) {
	server := models.ObaServer{ServerName: "test", ObaBaseURL: "https://test.example.com"}
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	if len(staticBundle.Agencies) == 0 || staticBundle.Agencies[0].Id == "" {
		t.Fatal("expected the fixture to declare an agency_id")
	}
	staticStore := NewStaticStore()

	err = storeStaticForServer(server, staticBundle, staticStore, geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	storeKey := models.ServerKey(server.ObaBaseURL, staticBundle.Agencies[0].Id)
	if _, ok := staticStore.Get(storeKey); !ok {
		t.Fatalf("expected the bundle to be stored under the declared agency key %s", storeKey)
	}
}

func TestGetEarliestAndLatestServiceDates(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-1", AgencyName: "test", ObaBaseURL: "https://test.example.com"}
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticData := models.NewStaticData(staticBundle)
	staticStore := NewStaticStore()
	staticStore.Set(server.ServerKey(), staticData)
	loc, _ := time.LoadLocation("America/Los_Angeles")
	// make sure these already exist in the test data
	expectedEarliestEndDate := time.Date(2024, 11, 22, 0, 0, 0, 0, loc)
	expectedLatestEndDate := time.Date(2025, 03, 28, 0, 0, 0, 0, loc)
	actualEarliest, actualLatest, err := getEarliestAndLatestServiceDates(staticData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !expectedEarliestEndDate.Equal(actualEarliest) {
		t.Fatalf("earliest date mismatch: expected %s, got %s", expectedEarliestEndDate.Format("2006-01-02"), actualEarliest.Format("2006-01-02"))
	}
	if !expectedLatestEndDate.Equal(actualLatest) {
		t.Fatalf("latest date mismatch: expected %s, got %s", expectedLatestEndDate.Format("2006-01-02"), actualLatest.Format("2006-01-02"))
	}
}

func TestGetStopLocationsByIDs(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-1", AgencyName: "test", ObaBaseURL: "https://test.example.com"}

	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticData := models.NewStaticData(staticBundle)
	staticStore := NewStaticStore()
	staticStore.Set(server.ServerKey(), staticData)

	t.Run("Valid stops IDs", func(t *testing.T) {
		stopIDs := []string{"11060", "1108"} // Make sure these exist in your test GTFS
		stops, err := getStopLocationsByIDs(server.ServerKey(), stopIDs, staticStore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) == 0 {
			t.Fatalf("expected some matched stops, got 0")
		}
	})

	t.Run("Invalid stop IDs", func(t *testing.T) {
		stopIDs := []string{"nonexistent1", "nonexistent2"}
		stops, err := getStopLocationsByIDs(server.ServerKey(), stopIDs, staticStore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(stops) != 0 {
			t.Errorf("expected 0 matched stops, got %d", len(stops))
		}
	})
}

func TestFetchAndStoreGTFSRTFeed(t *testing.T) {
	t.Run("Success Case", func(t *testing.T) {
		mockServer := setupGtfsServer(t, "gtfs_rt_feed_vehicles.pb")
		defer mockServer.Close()

		server := models.ObaServer{
			AgencyID: "",
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: mockServer.URL, GtfsRTAPIKey: "X-Test-Header", GtfsRTAPIValue: "test-value"},
			},
		}

		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		err := fetchAndStoreGTFSRTFeed(context.Background(), server, realtimeStore, client, nil)
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if realtimeStore.Get(server.ServerKey()) == nil {
			t.Fatalf("Expected realtimeStore to contain parsed GTFS-RT data, but it is nil")
		}

		data := readFixture(t, "gtfs_rt_feed_vehicles.pb")
		gtfsRT, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
		if err != nil {
			t.Fatalf("Failed to parse GTFS-RT data: %v", err)
		}
		expectedRtData := models.NewRealtimeData(gtfsRT)
		realtimeData := realtimeStore.Get(server.ServerKey())
		if realtimeData == nil {
			t.Fatal("realtimeData is nil; expected non-nil GTFS-RT data")
		}

		if len(expectedRtData.Vehicles) == 0 {
			t.Fatalf("Make sure that data contains at least one vehicle in the GTFS-RT feed in testdata/gtfs_rt_feed_vehicles.pb")
		}

		if len(realtimeData.Vehicles) != len(expectedRtData.Vehicles) {
			t.Fatalf("expected %d vehicles, got %d", len(expectedRtData.Vehicles), len(realtimeData.Vehicles))
		}

		expectedMap := make(map[string]remoteGtfs.Vehicle)
		for _, vehicle := range expectedRtData.Vehicles {
			if vehicle.Vehicle.ID != nil {
				expectedMap[vehicle.Vehicle.ID.ID] = vehicle.Vehicle
			}
		}
		countExpectedNilIDs := len(expectedRtData.Vehicles) - len(expectedMap)
		countNilIDs := 0
		byFeed := make(map[string]int)
		for _, realtimeVehicle := range realtimeData.Vehicles {
			byFeed[realtimeVehicle.FeedID]++
			if realtimeVehicle.Vehicle.ID != nil {
				expectedVehicle, exists := expectedMap[realtimeVehicle.Vehicle.ID.ID]
				if !exists {
					t.Errorf("Unexpected vehicle ID %s found in GTFS-RT data", realtimeVehicle.Vehicle.ID.ID)
				}
				assertVehicle(t, &realtimeVehicle.Vehicle, &expectedVehicle)
			} else {
				countNilIDs++
			}
		}
		if countNilIDs != countExpectedNilIDs {
			t.Errorf("Expected %d vehicles with nil IDs, got %d", countExpectedNilIDs, countNilIDs)
		}
		if len(byFeed) != 1 {
			t.Errorf("Expected vehicles from one feed, got %d: %v", len(byFeed), byFeed)
		}
		for feedID, count := range byFeed {
			if count != len(expectedRtData.Vehicles) {
				t.Errorf("Feed %s should contain %d vehicles, got %d", feedID, len(expectedRtData.Vehicles), count)
			}
		}
	})

	t.Run("Failure Case - Invalid URL", func(t *testing.T) {
		server := models.ObaServer{
			AgencyID:    "agency-2",
			GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "://invalid-url"}},
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()

		err := fetchAndStoreGTFSRTFeed(context.Background(), server, realtimeStore, client, nil)
		if err == nil {
			t.Error("Expected error due to invalid URL, got nil")
		}
	})

	t.Run("Failure Case - Closed Server", func(t *testing.T) {
		mockServer := httptest.NewServer(nil)
		mockServer.Close()

		server := models.ObaServer{
			AgencyID:    "agency-3",
			GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}},
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		err := fetchAndStoreGTFSRTFeed(context.Background(), server, realtimeStore, client, nil)
		if err == nil {
			t.Error("Expected error when accessing closed server, got nil")
		}
	})

	t.Run("Cancellation Case - No Failed Observation", func(t *testing.T) {
		// The feed hangs until the request context is canceled, simulating
		// a cycle deadline firing mid-fetch.
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer mockServer.Close()

		server := models.ObaServer{
			AgencyID:    "agency-cancel",
			GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}},
		}
		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		var mu sync.Mutex
		var failed int
		realtimeStore.SetObserver(func(_ models.ObaServer, observation FeedObservation) {
			mu.Lock()
			defer mu.Unlock()
			if !observation.Success {
				failed++
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		err := fetchAndStoreGTFSRTFeed(ctx, server, realtimeStore, client, nil)
		if err == nil {
			t.Error("Expected cancellation error so the vehicle pass stays gated, got nil")
		}
		mu.Lock()
		defer mu.Unlock()
		if failed != 0 {
			t.Errorf("Canceled fetch recorded %d failed observations; want none", failed)
		}
		if realtimeStore.Get(server.ServerKey()) != nil {
			t.Error("Canceled fetch published a snapshot; want nil")
		}
	})
}

// redirectRecorder records the value of one header from the requests it serves.
type redirectRecorder struct {
	mu    sync.Mutex
	value string
}

func (r *redirectRecorder) record(req *http.Request, header string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = req.Header.Get(header)
}

func (r *redirectRecorder) get() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value
}

// fetchFeedThroughRedirect runs one realtime fetch against feedURL with the
// configured API key, following whatever redirects the feed serves.
func fetchFeedThroughRedirect(t *testing.T, feedURL, key, value string, client *http.Client) (*models.RealtimeData, error) {
	t.Helper()
	server := models.ObaServer{
		AgencyID:    "redirect",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: feedURL, GtfsRTAPIKey: key, GtfsRTAPIValue: value}},
	}
	store := NewRealtimeStore()
	err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, client, nil)
	return store.Get(server.ServerKey()), err
}

func TestFetchAndStoreGTFSRTFeedRedirectsDoNotLeakCredentials(t *testing.T) {
	const (
		header = "X-Feed-Key"
		secret = "s3cret"
	)
	payload := marshalVehicleFeed(t, "redirected", 1, 2)
	newFeedServer := func(recorder *redirectRecorder) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r, header)
			// Writing to ResponseWriter in tests, error can be safely ignored.
			// #nosec G104
			w.Write(payload)
		}))
	}

	t.Run("same-origin redirect keeps the credential", func(t *testing.T) {
		recorder := &redirectRecorder{}
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
			recorder.record(r, header)
			// #nosec G104
			w.Write(payload)
		})
		mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/feed", http.StatusFound)
		})
		source := httptest.NewServer(mux)
		defer source.Close()

		data, err := fetchFeedThroughRedirect(t, source.URL+"/redirect", header, secret, source.Client())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if data == nil {
			t.Fatal("redirected fetch published no snapshot")
		}
		if got := recorder.get(); got != secret {
			t.Errorf("same-origin redirect sent %q, want %q", got, secret)
		}
	})

	t.Run("cross-origin redirect drops the credential", func(t *testing.T) {
		recorder := &redirectRecorder{}
		destination := newFeedServer(recorder)
		defer destination.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}))
		defer source.Close()

		data, err := fetchFeedThroughRedirect(t, source.URL, header, secret, source.Client())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if data == nil {
			t.Fatal("redirected fetch published no snapshot")
		}
		if got := recorder.get(); got != "" {
			t.Errorf("cross-origin redirect sent %q, want no credential", got)
		}
	})

	t.Run("cross-origin redirect without authentication still works", func(t *testing.T) {
		recorder := &redirectRecorder{}
		destination := newFeedServer(recorder)
		defer destination.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}))
		defer source.Close()

		data, err := fetchFeedThroughRedirect(t, source.URL, "", "", source.Client())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if data == nil {
			t.Fatal("redirected fetch published no snapshot")
		}
	})

	t.Run("HTTPS to HTTP downgrade drops the credential", func(t *testing.T) {
		recorder := &redirectRecorder{}
		destination := newFeedServer(recorder)
		defer destination.Close()
		source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}))
		defer source.Close()

		data, err := fetchFeedThroughRedirect(t, source.URL, header, secret, source.Client())
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if data == nil {
			t.Fatal("redirected fetch published no snapshot")
		}
		if got := recorder.get(); got != "" {
			t.Errorf("HTTPS to HTTP redirect sent %q, want no credential", got)
		}
	})

	t.Run("redirect loop is stopped", func(t *testing.T) {
		loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/loop", http.StatusFound)
		}))
		defer loop.Close()

		_, err := fetchFeedThroughRedirect(t, loop.URL+"/loop", header, secret, loop.Client())
		if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
			t.Fatalf("redirect loop error = %v, want the redirect limit", err)
		}
	})

	t.Run("invalid redirect destination fails the fetch", func(t *testing.T) {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "://not-a-url")
			w.WriteHeader(http.StatusFound)
		}))
		defer broken.Close()

		if _, err := fetchFeedThroughRedirect(t, broken.URL, header, secret, broken.Client()); err == nil {
			t.Fatal("expected an invalid redirect destination to fail the fetch")
		}
	})
}

func TestFetchAndStoreGTFSRTFeedKeepsAgenciesIsolated(t *testing.T) {
	mockServer := setupGtfsServer(t, "gtfs_rt_feed_vehicles.pb")
	defer mockServer.Close()

	store := NewRealtimeStore()
	client := &http.Client{Timeout: 5 * time.Second}
	first := models.ObaServer{AgencyID: "agency-1", ObaBaseURL: "https://first.example.com", GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}}}
	second := models.ObaServer{AgencyID: "agency-2", ObaBaseURL: "https://second.example.com", GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}}}
	index := NewRouteAgencyIndex()
	index.Replace(first.ServerKey(), nil, nil, nil)
	index.Replace(second.ServerKey(), nil, nil, nil)

	if err := fetchAndStoreGTFSRTFeed(context.Background(), first, store, client, index); err != nil {
		t.Fatalf("fetch first agency feed: %v", err)
	}
	firstData := store.Get(first.ServerKey())
	if err := fetchAndStoreGTFSRTFeed(context.Background(), second, store, client, index); err != nil {
		t.Fatalf("fetch second agency feed: %v", err)
	}
	if firstData == nil || store.Get(second.ServerKey()) == nil {
		t.Fatal("expected realtime data for both agencies")
	}
	if store.Get(first.ServerKey()) != firstData {
		t.Fatal("second agency fetch replaced the first agency's realtime data")
	}
}

func TestFetchAndStoreGTFSRTFeedKeepsDistinctServersWithSameAgencyIDIsolated(t *testing.T) {
	mockServer := setupGtfsServer(t, "gtfs_rt_feed_vehicles.pb")
	defer mockServer.Close()

	store := NewRealtimeStore()
	client := &http.Client{Timeout: 5 * time.Second}
	first := models.ObaServer{AgencyID: "shared-1", ObaBaseURL: "https://first.example.com", GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}}}
	second := models.ObaServer{AgencyID: "shared-1", ObaBaseURL: "https://second.example.com", GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: mockServer.URL}}}

	if first.ServerKey() == second.ServerKey() {
		t.Fatalf("distinct deployments must not share a server key, got %q for both", first.ServerKey())
	}

	index := NewRouteAgencyIndex()
	index.Replace(first.ServerKey(), nil, nil, nil)
	index.Replace(second.ServerKey(), nil, nil, nil)
	if err := fetchAndStoreGTFSRTFeed(context.Background(), first, store, client, index); err != nil {
		t.Fatalf("fetch first deployment feed: %v", err)
	}
	firstData := store.Get(first.ServerKey())
	if err := fetchAndStoreGTFSRTFeed(context.Background(), second, store, client, index); err != nil {
		t.Fatalf("fetch second deployment feed: %v", err)
	}
	if firstData == nil || store.Get(second.ServerKey()) == nil {
		t.Fatal("expected realtime data for both deployments sharing an agency_id")
	}
	if store.Get(first.ServerKey()) != firstData {
		t.Fatal("second deployment fetch replaced the first deployment's realtime data")
	}
}

func TestFetchAndStoreGTFSRTFeedRejectsMultipleSources(t *testing.T) {
	transport := &countingRoundTripper{body: marshalVehicleFeed(t, "101", 1, 2)}
	server := models.ObaServer{GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "https://a.example"}, {VehiclePositionURL: "https://b.example"}}}
	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, &http.Client{Transport: transport}, nil); err == nil {
		t.Fatal("expected multiple realtime sources to be rejected")
	}
	if transport.calls != 0 || store.Get(server.ServerKey()) != nil {
		t.Fatal("invalid configuration must neither fetch nor publish")
	}
}

func marshalVehicleFeed(t *testing.T, id string, lat, lon float32) []byte {
	t.Helper()

	feed := &gtfsrt.FeedMessage{
		Header: &gtfsrt.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           proto.Uint64(1_700_000_000),
		},
		Entity: []*gtfsrt.FeedEntity{
			{
				Id: proto.String("1"),
				Vehicle: &gtfsrt.VehiclePosition{
					Vehicle:   &gtfsrt.VehicleDescriptor{Id: proto.String(id)},
					Position:  &gtfsrt.Position{Latitude: proto.Float32(lat), Longitude: proto.Float32(lon)},
					Timestamp: proto.Uint64(1_700_000_000),
				},
			},
		},
	}
	data, err := proto.Marshal(feed)
	if err != nil {
		t.Fatalf("marshal GTFS-RT feed: %v", err)
	}
	return data
}

func serveBytes(t *testing.T, data []byte) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Writing to ResponseWriter in tests, error can be safely ignored.
		// #nosec G104
		w.Write(data)
	}))
}

// countingRoundTripper records every request that goes through it so tests
// can assert how many times an upstream was hit.
type countingRoundTripper struct {
	mu    sync.Mutex
	calls int
	body  []byte
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(c.body)),
		Header:     make(http.Header),
	}, nil
}

func TestFetchAndStoreGTFSRTFeedSingleFetchUnderServerKey(t *testing.T) {
	// Server-mode: the entry names no agency, so the merged feed is stored
	// under the server-scoped key and fetched exactly once per tick. The
	// vehicle pass reads that one key and attributes vehicles itself, so
	// there is nothing to register per agency.
	body := marshalVehicleFeed(t, "shared-vehicle", 1, 2)
	rt := &countingRoundTripper{body: body}
	client := &http.Client{Transport: rt}

	server := models.ObaServer{
		ServerName:      "shared",
		ObaBaseURL:      "https://example.com",
		ObaApiKey:       "key",
		GtfsStaticFeeds: []string{"https://example.com/gtfs.zip"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://rt.example.com/vehicles.pb",
		}},
	}

	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, client, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rt.calls != 1 {
		t.Fatalf("expected 1 RT fetch per tick, got %d", rt.calls)
	}

	stored := store.Get(server.ServerKey())
	if stored == nil {
		t.Fatalf("expected the merged feed under the server-scoped key %q", server.ServerKey())
	}
	if got := len(stored.Vehicles); got != 1 {
		t.Fatalf("expected 1 vehicle, got %d", got)
	}
	// Nothing should be registered under an agency's key: agencies are
	// resolved by the vehicle pass, not by the fetch.
	if store.Get(models.ServerKey(server.ObaBaseURL, "agency-A")) != nil {
		t.Fatal("expected no per-agency realtime entries in server-mode")
	}
}

func TestFetchAndStoreGTFSRTFeedAgencyModeUsesAgencyKey(t *testing.T) {
	body := marshalVehicleFeed(t, "shim-vehicle", 1, 2)
	rt := &countingRoundTripper{body: body}
	client := &http.Client{Transport: rt}

	server := models.ObaServer{
		ServerName:      "shim",
		AgencyID:        "agency-shim",
		AgencyName:      "Agency Shim",
		ObaBaseURL:      "https://example.com",
		ObaApiKey:       "key",
		GtfsStaticFeeds: []string{"https://example.com/gtfs.zip"},
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://rt.example.com/vehicles.pb",
		}},
	}
	store := NewRealtimeStore()
	index := NewRouteAgencyIndex()
	index.Replace(server.ServerKey(), nil, nil, nil)

	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, client, index); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("expected 1 fetch, got %d", rt.calls)
	}
	got := store.Get(server.ServerKey())
	if got == nil {
		t.Fatal("expected the feed to populate server.ServerKey()")
	}
	if len(got.Vehicles) != 1 {
		t.Fatalf("expected the unresolvable vehicle to be kept when no other agency is known, got %d", len(got.Vehicles))
	}
}

func TestFetchAndStoreGTFSRTFeedAgencyModeKeepsUnresolvedVehiclesInSingleAgencyFeed(t *testing.T) {
	feed := marshalRoutedVehicleFeed(t, []routedTestVehicle{
		{ID: "owned", RouteID: "route-a", TripID: "trip-a"},
		{ID: "deadhead"},
		{ID: "stale", RouteID: "not-in-bundle", TripID: "not-in-bundle"},
	})
	rtServer := serveBytes(t, feed)
	defer rtServer.Close()

	server := models.ObaServer{
		ServerName: "agency-a", AgencyID: "agency-a", ObaBaseURL: "https://example.com",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}},
	}
	index := NewRouteAgencyIndex()
	index.Replace(server.ServerKey(), map[string]string{"route-a": "agency-a"}, map[string]string{"trip-a": "agency-a"}, nil)
	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, &http.Client{}, index); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if data := store.Get(server.ServerKey()); data == nil || len(data.Vehicles) != 3 {
		t.Fatalf("expected all 3 vehicles kept in a single-agency feed, got %+v", data)
	}
}

func TestFetchAndStoreGTFSRTFeedAgencyModePublishesUnfilteredWithoutStaticSnapshot(t *testing.T) {
	feed := marshalRoutedVehicleFeed(t, []routedTestVehicle{
		{ID: "a", RouteID: "route-a"},
		{ID: "b", RouteID: "route-b"},
	})
	rtServer := serveBytes(t, feed)
	defer rtServer.Close()

	server := models.ObaServer{
		ServerName: "agency-a", AgencyID: "agency-a", ObaBaseURL: "https://example.com",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}},
	}
	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, &http.Client{}, NewRouteAgencyIndex()); err != nil {
		t.Fatalf("a missing static snapshot must not fail the realtime fetch: %v", err)
	}
	if data := store.Get(server.ServerKey()); data == nil || len(data.Vehicles) != 2 {
		t.Fatalf("expected the unfiltered feed to be published, got %+v", data)
	}
}

func TestFetchAndStoreGTFSRTFeedFiltersAgencyVehiclesByRouteOrTrip(t *testing.T) {
	feed := marshalRoutedVehicleFeed(t, []routedTestVehicle{
		{ID: "both-a", RouteID: "route-a", TripID: "trip-a"},
		{ID: "route-a", RouteID: "route-a"},
		{ID: "trip-a", TripID: "trip-a"},
		{ID: "foreign", RouteID: "route-b", TripID: "trip-b"},
		{ID: "trip-fallback", RouteID: "unknown", TripID: "trip-a"},
		{ID: "route-fallback", RouteID: "route-a", TripID: "unknown"},
		{ID: "conflict", RouteID: "route-a", TripID: "trip-b"},
		{ID: "unknown"},
	})
	rtServer := serveBytes(t, feed)
	defer rtServer.Close()

	server := models.ObaServer{
		ServerName: "agency-a", AgencyID: "agency-a", ObaBaseURL: "https://example.com",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}},
	}
	index := NewRouteAgencyIndex()
	index.Replace(server.ServerKey(),
		map[string]string{"route-a": "agency-a", "route-b": "agency-b"},
		map[string]string{"trip-a": "agency-a", "trip-b": "agency-b"},
		nil)
	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(context.Background(), server, store, &http.Client{}, index); err != nil {
		t.Fatalf("fetch and filter: %v", err)
	}
	data := store.Get(server.ServerKey())
	if data == nil || len(data.Vehicles) != 5 {
		t.Fatalf("expected 5 agency-a vehicles after filtering, got %+v", data)
	}
	for _, vehicle := range data.Vehicles {
		if vehicle.FeedID != "0" {
			t.Fatalf("expected original feed ID to be preserved, got %q", vehicle.FeedID)
		}
	}
}

type routedTestVehicle struct {
	ID      string
	RouteID string
	TripID  string
}

func marshalRoutedVehicleFeed(t *testing.T, vehicles []routedTestVehicle) []byte {
	t.Helper()
	entities := make([]*gtfsrt.FeedEntity, 0, len(vehicles))
	for _, vehicle := range vehicles {
		entity := &gtfsrt.FeedEntity{Id: proto.String(vehicle.ID), Vehicle: &gtfsrt.VehiclePosition{
			Vehicle:  &gtfsrt.VehicleDescriptor{Id: proto.String(vehicle.ID)},
			Position: &gtfsrt.Position{Latitude: proto.Float32(47), Longitude: proto.Float32(-122)},
		}}
		if vehicle.RouteID != "" || vehicle.TripID != "" {
			entity.Vehicle.Trip = &gtfsrt.TripDescriptor{RouteId: proto.String(vehicle.RouteID), TripId: proto.String(vehicle.TripID)}
		}
		entities = append(entities, entity)
	}
	data, err := proto.Marshal(&gtfsrt.FeedMessage{Header: &gtfsrt.FeedHeader{GtfsRealtimeVersion: proto.String("2.0")}, Entity: entities})
	if err != nil {
		t.Fatalf("marshal routed feed: %v", err)
	}
	return data
}

// floatPtr returns a pointer to f. Used to build *float64 stop lat/lons for
// the synthetic bundles below without having to declare locals at every
// call site.
func floatPtr(f float64) *float64 { return &f }

// makeSyntheticBundle builds a *remoteGtfs.Static containing one agency row
// plus stops for bounding-box and snapshot tests.
func makeSyntheticBundle(t *testing.T, agencyID, agencyName, agencyURL string, stops []remoteGtfs.Stop) *remoteGtfs.Static {
	t.Helper()
	return &remoteGtfs.Static{
		Agencies: []remoteGtfs.Agency{{
			Id:   agencyID,
			Name: agencyName,
			Url:  agencyURL,
		}},
		Stops: stops,
	}
}

func TestComputeBoundingBoxesForMultiAgencyFeedUsesSharedStops(t *testing.T) {
	bundle := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A", Latitude: floatPtr(1), Longitude: floatPtr(2)},
		{Id: "B", Latitude: floatPtr(3), Longitude: floatPtr(4)},
	})
	bundle.Agencies = append(bundle.Agencies, remoteGtfs.Agency{Id: "agency-B", Name: "Agency B", Url: "https://b.example"})

	computed := computeBoundingBoxes(bundle)
	want := geo.BoundingBox{MinLat: 1, MaxLat: 3, MinLon: 2, MaxLon: 4}
	for _, agencyID := range []string{"agency-A", "agency-B"} {
		if got := computed.byAgency[agencyID]; got != want {
			t.Fatalf("expected shared bounds for %s to be %+v, got %+v", agencyID, want, got)
		}
	}
	if computed.unionErr != nil || computed.union != want {
		t.Fatalf("expected union bounds %+v, got %+v (error=%v)", want, computed.union, computed.unionErr)
	}
}

func TestStoreStaticForServerStoresServerScopedBoundingBox(t *testing.T) {
	bundle := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "S1", Latitude: floatPtr(47.60), Longitude: floatPtr(-122.30)},
		{Id: "S2", Latitude: floatPtr(47.70), Longitude: floatPtr(-122.20)},
	})

	// No AgencyID: a server-scoped entry.
	server := models.ObaServer{
		ServerName:      "multi",
		ObaBaseURL:      "https://example.com",
		GtfsStaticFeeds: []string{"https://example.com/gtfs.zip"},
	}

	boundingBoxStore := geo.NewBoundingBoxStore()
	err := storeStaticForServer(server, bundle, NewStaticStore(), boundingBoxStore, NewRouteAgencyIndex(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("storeStaticForServer: %v", err)
	}

	serverScoped, ok := boundingBoxStore.Get(server.ServerKey())
	if !ok {
		t.Fatalf("expected a bounding box under the server-scoped key %q", server.ServerKey())
	}
	perAgency, ok := boundingBoxStore.Get(models.ServerKey(server.ObaBaseURL, "agency-A"))
	if !ok {
		t.Fatal("expected a bounding box under the agency key")
	}
	if serverScoped != perAgency {
		t.Fatalf("expected the server-scoped box to match the agency box, got %+v vs %+v", serverScoped, perAgency)
	}
}

func TestStoreStaticForServerMergedFeedSharesBoundsAndStorage(t *testing.T) {
	bundle := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A-S1", Latitude: floatPtr(47.6), Longitude: floatPtr(-122.3)},
		{Id: "B-S1", Latitude: floatPtr(40.6), Longitude: floatPtr(-73.3)},
	})
	bundle.Agencies = append(bundle.Agencies, remoteGtfs.Agency{Id: "agency-B", Name: "Agency B"})
	server := models.ObaServer{ServerName: "merged", ObaBaseURL: "https://merged.example"}
	store, boxes := NewStaticStore(), geo.NewBoundingBoxStore()
	if err := storeStaticForServer(server, bundle, store, boxes, NewRouteAgencyIndex(), nil, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	want := geo.BoundingBox{MinLat: 40.6, MaxLat: 47.6, MinLon: -122.3, MaxLon: -73.3}
	first, ok := store.Get(models.ServerKey(server.ObaBaseURL, "agency-A"))
	if !ok {
		t.Fatal("missing first agency snapshot")
	}
	for _, agency := range []string{"agency-A", "agency-B"} {
		key := models.ServerKey(server.ObaBaseURL, agency)
		if data, ok := store.Get(key); !ok || data != first {
			t.Fatalf("missing shared snapshot for %s", agency)
		}
		if got, ok := boxes.Get(key); !ok || got != want {
			t.Fatalf("bounds for %s = %+v, want %+v", agency, got, want)
		}
	}
	if got, ok := boxes.Get(server.ServerKey()); !ok || got != want {
		t.Fatalf("server bounds = %+v, want %+v", got, want)
	}
}

func TestStoreStaticForServerObservesBundleWithoutUsableCoordinates(t *testing.T) {
	bundle := makeSyntheticBundle(t, "declared-agency", "Declared Agency", "https://a.example", []remoteGtfs.Stop{{Id: "S1"}})
	server := models.ObaServer{ServerName: "invalid", ObaBaseURL: "https://invalid.example", AgencyID: "configured-agency"}
	observed := 0
	var logBuffer bytes.Buffer
	observer := func(models.ObaServer, string, string, *models.StaticData) {
		observed++
	}

	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	if err := storeStaticForServer(server, bundle, NewStaticStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), observer, logger); err != nil {
		t.Fatalf("storeStaticForServer: %v", err)
	}
	if observed != 1 {
		t.Fatalf("expected observer once despite bounding-box failure, got %d calls", observed)
	}
	if !bytes.Contains(logBuffer.Bytes(), []byte("Could not compute agency bounding box")) {
		t.Fatalf("expected agency bounding-box error to be logged, got %q", logBuffer.String())
	}
}

func TestComputeAgencyBoundingBoxesReportsCoordinateErrors(t *testing.T) {
	bundle := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{{Id: "S1"}})
	computed := computeBoundingBoxes(bundle)
	if _, ok := computed.byAgency["agency-A"]; ok {
		t.Fatal("expected no bounding box for stops without coordinates")
	}
	if computed.errorsByAgency["agency-A"] == nil {
		t.Fatal("expected agency coordinate error to be reported")
	}
}

func TestStoreStaticForServerSkipsServerScopedBoxInAgencyMode(t *testing.T) {
	bundle := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "S1", Latitude: floatPtr(47.60), Longitude: floatPtr(-122.30)},
	})

	server := models.ObaServer{
		ServerName:      "solo",
		AgencyID:        "agency-A",
		AgencyName:      "Agency A",
		ObaBaseURL:      "https://example.com",
		GtfsStaticFeeds: []string{"https://example.com/gtfs.zip"},
	}

	boundingBoxStore := geo.NewBoundingBoxStore()
	err := storeStaticForServer(server, bundle, NewStaticStore(), boundingBoxStore, NewRouteAgencyIndex(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("storeStaticForServer: %v", err)
	}

	if _, ok := boundingBoxStore.Get(models.ServerKey(server.ObaBaseURL, "")); ok {
		t.Fatal("agency-mode should not publish a server-scoped bounding box")
	}
}

type gtfsRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f gtfsRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingGTFSBody struct {
	err error
}

func (b failingGTFSBody) Read([]byte) (int, error) {
	return 0, b.err
}

func (b failingGTFSBody) Close() error {
	return nil
}

func assertGTFSRedaction(t *testing.T, rec *report.RecordingTransport, err error, safeURL string, secrets ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected an error")
	}

	// Verify that the sanitized error is safe to include in structured logs.
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	logger.Error("GTFS operation failed", "error", err)

	logOutput := logBuffer.String()

	for _, secret := range secrets {
		if strings.Contains(logOutput, secret) {
			t.Errorf("credential %q leaked in log output", secret)
		}
	}

	if !strings.Contains(logOutput, safeURL) {
		t.Errorf("sanitized endpoint %q missing from log output", safeURL)
	}

	// Verify the captured Sentry event.
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected one Sentry event, got %d", len(events))
	}

	eventJSON, marshalErr := json.Marshal(events[0])
	if marshalErr != nil {
		t.Fatalf("marshal Sentry event: %v", marshalErr)
	}

	combined := err.Error() + string(eventJSON)

	for _, secret := range secrets {
		if strings.Contains(combined, secret) {
			t.Errorf("credential %q leaked in returned error or Sentry event", secret)
		}
	}

	if !strings.Contains(combined, safeURL) {
		t.Errorf("sanitized endpoint %q missing from diagnostics: %s", safeURL, combined)
	}
}

func TestParseGTFSRTFeedRedactsCredentialsFromFetchError(t *testing.T) {
	rec := report.CaptureSentry(t)

	const (
		username   = "rt-user-sentinel"
		password   = "rt-password-sentinel"
		queryKey   = "rt-query-secret-sentinel"
		headerKey  = "rt-header-secret-sentinel"
		headerName = "X-Feed-Key"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/vehicles.pb?api_key=" + queryKey

	client := &http.Client{
		Transport: gtfsRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("synthetic transport failure: " + headerKey)
		}),
	}

	server := models.ObaServer{
		ServerName: "redaction-test",
		AgencyID:   "agency-redaction",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: rawURL,
			GtfsRTAPIKey:       headerName,
			GtfsRTAPIValue:     headerKey,
		}},
	}

	_, _, err := parseGTFSRTFeed(
		context.Background(),
		server,
		client,
		time.Now(),
	)

	assertGTFSRedaction(
		t,
		rec,
		err,
		"https://feed.example.com/vehicles.pb",
		username,
		password,
		queryKey,
		headerKey,
	)
}

func TestParseGTFSRTFeedRedactsCredentialsFromHTTPStatusError(t *testing.T) {
	rec := report.CaptureSentry(t)

	const (
		username  = "status-user-sentinel"
		password  = "status-password-sentinel"
		queryKey  = "status-query-secret-sentinel"
		headerKey = "status-header-secret-sentinel"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/vehicles.pb?api_key=" + queryKey

	client := &http.Client{
		Transport: gtfsRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 " + headerKey,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	}

	server := models.ObaServer{
		ServerName: "status-redaction-test",
		AgencyID:   "agency-status",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: rawURL,
			GtfsRTAPIKey:       "X-Feed-Key",
			GtfsRTAPIValue:     headerKey,
		}},
	}

	_, _, err := parseGTFSRTFeed(
		context.Background(),
		server,
		client,
		time.Now(),
	)

	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected HTTP 503 diagnostic, got %v", err)
	}

	assertGTFSRedaction(
		t,
		rec,
		err,
		"https://feed.example.com/vehicles.pb",
		username,
		password,
		queryKey,
		headerKey,
	)
}

func TestParseGTFSRTFeedRedactsCredentialsFromReadError(t *testing.T) {
	rec := report.CaptureSentry(t)

	const (
		username  = "read-user-sentinel"
		password  = "read-password-sentinel"
		queryKey  = "read-query-secret-sentinel"
		headerKey = "read-header-secret-sentinel"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/vehicles.pb?api_key=" + queryKey

	client := &http.Client{
		Transport: gtfsRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: failingGTFSBody{
					err: errors.New("synthetic read failure: " + headerKey),
				},
				Request: req,
			}, nil
		}),
	}

	server := models.ObaServer{
		ServerName: "read-redaction-test",
		AgencyID:   "agency-read",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: rawURL,
			GtfsRTAPIKey:       "X-Feed-Key",
			GtfsRTAPIValue:     headerKey,
		}},
	}

	_, _, err := parseGTFSRTFeed(
		context.Background(),
		server,
		client,
		time.Now(),
	)

	assertGTFSRedaction(
		t,
		rec,
		err,
		"https://feed.example.com/vehicles.pb",
		username,
		password,
		queryKey,
		headerKey,
	)
}

func TestFetchGTFSBundleDataRedactsCredentials(t *testing.T) {
	const (
		username     = "static-user-sentinel"
		password     = "static-password-sentinel"
		queryKey     = "static-query-secret-sentinel"
		statusSecret = "static-status-secret-sentinel"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/static.zip?api_key=" + queryKey

	safeURL := "https://feed.example.com/static.zip"

	tests := []struct {
		name       string
		transport  gtfsRoundTripperFunc
		wantStage  StaticFailureStage
		wantReason StaticFailureReason
		wantStatus int
	}{
		{
			name: "network failure",
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("synthetic network failure")
			},
			wantStage:  StaticFailureRequest,
			wantReason: StaticReasonConnection,
		},
		{
			name: "HTTP status failure",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Status:     "503 " + statusSecret,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			},
			wantStage:  StaticFailureHTTP,
			wantReason: StaticReasonServerError,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "response body read failure",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: failingGTFSBody{
						err: errors.New("synthetic body read failure"),
					},
					Request: req,
				}, nil
			},
			wantStage:  StaticFailureDownload,
			wantReason: StaticReasonConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := report.CaptureSentry(t)

			client := &http.Client{
				Transport: tt.transport,
			}

			_, err := fetchGTFSBundleData(
				context.Background(),
				client,
				rawURL,
				"agency-static",
				1,
			)

			assertGTFSRedaction(
				t,
				rec,
				err,
				safeURL,
				username,
				password,
				queryKey,
				statusSecret,
			)

			var failure *StaticFeedError
			if !errors.As(err, &failure) {
				t.Fatalf("expected StaticFeedError, got %T: %v", err, err)
			}

			if failure.Stage != tt.wantStage {
				t.Errorf("Stage = %q, want %q", failure.Stage, tt.wantStage)
			}

			if failure.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", failure.Reason, tt.wantReason)
			}

			if failure.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", failure.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestParseDownloadedGTFSBundleRedactsCredentials(t *testing.T) {
	rec := report.CaptureSentry(t)

	const (
		username = "parser-user-sentinel"
		password = "parser-password-sentinel"
		queryKey = "parser-query-secret-sentinel"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/static.zip?api_key=" + queryKey

	safeURL := "https://feed.example.com/static.zip"

	originalFailure := &StaticFeedError{
		FeedURL:     rawURL,
		Stage:       StaticFailureParse,
		Reason:      StaticReasonDuplicateID,
		StatusCode:  422,
		RetryAfter:  15 * time.Second,
		ContentHash: "test-content-hash",
		Err: errors.New(
			"failed to parse static feed from " + rawURL,
		),
	}

	parser := func(
		data []byte,
		url string,
		agencyID string,
	) (*remoteGtfs.Static, error) {
		if url != rawURL {
			t.Fatalf("parser URL = %q, want original configured URL", url)
		}
		return nil, originalFailure
	}

	_, err := parseDownloadedGTFSBundle(
		parser,
		[]byte("test static feed"),
		rawURL,
		"agency-static",
	)

	assertGTFSRedaction(
		t,
		rec,
		err,
		safeURL,
		username,
		password,
		queryKey,
	)

	var failure *StaticFeedError
	if !errors.As(err, &failure) {
		t.Fatalf("expected StaticFeedError, got %T: %v", err, err)
	}

	if failure == originalFailure {
		t.Fatal("expected a sanitized copy, not the original error")
	}

	if failure.FeedURL != rawURL {
		t.Error("internal FeedURL identity should remain unchanged")
	}

	if failure.Stage != originalFailure.Stage {
		t.Errorf("Stage = %q, want %q", failure.Stage, originalFailure.Stage)
	}

	if failure.Reason != originalFailure.Reason {
		t.Errorf("Reason = %q, want %q", failure.Reason, originalFailure.Reason)
	}

	if failure.StatusCode != originalFailure.StatusCode {
		t.Errorf("StatusCode = %d, want %d", failure.StatusCode, originalFailure.StatusCode)
	}

	if failure.RetryAfter != originalFailure.RetryAfter {
		t.Errorf("RetryAfter = %v, want %v", failure.RetryAfter, originalFailure.RetryAfter)
	}

	if failure.ContentHash != originalFailure.ContentHash {
		t.Errorf("ContentHash = %q, want %q", failure.ContentHash, originalFailure.ContentHash)
	}
}

func TestStoreStaticForServer_NoDeclaredAgenciesSanitizesURL(t *testing.T) {
	rec := report.CaptureSentry(t)

	const (
		obaUser  = "oba-user-sentinel"
		obaPass  = "oba-pass-sentinel"
		obaToken = "oba-token-sentinel"
	)
	rawObaURL := fmt.Sprintf("https://%s:%s@oba.example.com/api?token=%s", obaUser, obaPass, obaToken)
	safeObaURL := "https://oba.example.com/api"

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	server := models.ObaServer{
		ServerName: "test-server",
		AgencyID:   "",
		ObaBaseURL: rawObaURL,
		ObaApiKey:  "oba-api-key",
	}

	bundle := &remoteGtfs.Static{}
	err := storeStaticForServer(server, bundle, NewStaticStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), nil, logger)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	for _, secret := range []string{obaUser, obaPass, obaToken, "oba-api-key"} {
		if strings.Contains(errStr, secret) {
			t.Errorf("returned error leaked credential %q: %s", secret, errStr)
		}
	}
	if !strings.Contains(errStr, safeObaURL) {
		t.Errorf("returned error does not contain safeObaURL %q: %s", safeObaURL, errStr)
	}

	logStr := logBuf.String()
	for _, secret := range []string{obaUser, obaPass, obaToken} {
		if strings.Contains(logStr, secret) {
			t.Errorf("log leaked credential %q", secret)
		}
	}
	if !strings.Contains(logStr, safeObaURL) {
		t.Errorf("log does not contain safeObaURL %q: %s", safeObaURL, logStr)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 Sentry event, got %d", len(events))
	}
	event := events[0]
	if len(event.Exception) > 0 {
		if !strings.Contains(event.Exception[0].Value, safeObaURL) {
			t.Errorf("exception does not contain safeObaURL %q: %v", safeObaURL, event.Exception[0].Value)
		}
	}

	for _, secret := range []string{obaUser, obaPass, obaToken, "oba-api-key"} {
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

// A server-mode bundle without usable coordinates must not keep the previous
// bundle's boxes: vehicle checks would use them, and the static version would
// record a box it never produced.
func TestStoreStaticSnapshotServerModeDropsBoxesWithoutUsableCoordinates(t *testing.T) {
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: "https://example.com"}
	agencyKey := models.ServerKey(server.ObaBaseURL, "agency-A")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, boxes := NewStaticStore(), geo.NewBoundingBoxStore()

	good := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "S1", Latitude: floatPtr(47.60), Longitude: floatPtr(-122.30)},
	})
	if _, err := storeStaticSnapshot(server, good, store, boxes, NewRouteAgencyIndex(), nil, logger); err != nil {
		t.Fatalf("store good bundle: %v", err)
	}
	if _, ok := boxes.Get(agencyKey); !ok {
		t.Fatal("expected a box from the good bundle")
	}

	broken := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{{Id: "S1"}})
	published, err := storeStaticSnapshot(server, broken, store, boxes, NewRouteAgencyIndex(), nil, logger)
	if err != nil {
		t.Fatalf("store broken bundle: %v", err)
	}
	for _, key := range []string{agencyKey, server.ServerKey()} {
		if got, ok := boxes.Get(key); ok {
			t.Errorf("box for %q = %+v, want none after a bundle without coordinates", key, got)
		}
	}
	if len(published.boxes) != 0 {
		t.Errorf("published boxes = %+v, want none", published.boxes)
	}
}
