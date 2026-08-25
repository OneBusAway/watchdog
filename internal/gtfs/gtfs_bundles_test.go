package gtfs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
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
	downloadGTFSBundles(ctx, client, servers, logger, boundingBoxStore, staticStore, NewRouteAgencyIndex(), nil, 1)

}

func TestRefreshGTFSBundles(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	servers := []models.ObaServer{{AgencyID: "agency-1", AgencyName: "Test Server", GtfsStaticFeeds: []string{"http://example.com/gtfs.zip"}}}
	boundingBoxStore := geo.NewBoundingBoxStore()
	staticStore := NewStaticStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	go refreshGTFSBundles(ctx, client, servers, logger, 10*time.Millisecond, boundingBoxStore, staticStore, NewRouteAgencyIndex(), nil, 1)

	time.Sleep(15 * time.Millisecond)

	t.Log("refreshGTFSBundles executed without crashing")
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
	server := models.ObaServer{ServerName: "test", AgencyID: "agency-1", AgencyName: "test", ObaBaseURL: "https://test.example.com"}
	data := readFixture(t, "gtfs.zip")
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatal("failed to parse gtfs static data")
	}
	staticStore := NewStaticStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	routeAgencyIndex := NewRouteAgencyIndex()

	err = storeStaticForServer(server, []*remoteGtfs.Static{staticBundle}, staticStore, boundingBoxStore, routeAgencyIndex, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// storeStaticForServer keys bundles by the agency declared in agency.txt,
	// not by the configured agency_id. Look up the bundle's actual agency_id
	// (gtfs.zip ships agency_id "40") to retrieve the fetch time.
	if len(staticBundle.Agencies) == 0 {
		t.Fatal("expected the fixture to declare at least one agency")
	}
	declaredAgency := staticBundle.Agencies[0].Id
	if declaredAgency == "" {
		t.Fatal("expected the fixture's agency to declare an agency_id")
	}
	storeKey := models.ServerKey(server.ObaBaseURL, declaredAgency)
	fetchTime, ok := staticStore.GetFetchTime(storeKey)
	if !ok {
		t.Fatalf("expected a fetch time to be recorded for server key %s", storeKey)
	}
	if time.Since(fetchTime) > time.Minute {
		t.Fatalf("fetch time should be recent, got %s", fetchTime)
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
			AgencyID: "agency-1",
			GtfsRTFeeds: []models.GtfsRTFeed{
				{VehiclePositionURL: mockServer.URL, GtfsRTAPIKey: "X-Test-Header", GtfsRTAPIValue: "test-value"},
				{VehiclePositionURL: mockServer.URL},
			},
		}

		client := &http.Client{
			Timeout: 5 * time.Second,
		}
		realtimeStore := NewRealtimeStore()
		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
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

		// The test server is configured with two feeds pointing at the same URL.
		// Each feed is an independent vehicle namespace, so every vehicle is
		// retained once per feed: the merged count is twice the single-feed
		// count. A shared cross-feed dedup map would have collapsed this to 1x,
		// silently dropping distinct vehicles.
		if len(realtimeData.Vehicles) != 2*len(expectedRtData.Vehicles) {
			t.Fatalf("Expected %d vehicles (2 feeds), got %d", 2*len(expectedRtData.Vehicles), len(realtimeData.Vehicles))
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
		if countNilIDs != 2*countExpectedNilIDs {
			t.Errorf("Expected %d vehicles with nil IDs, got %d", 2*countExpectedNilIDs, countNilIDs)
		}
		if len(byFeed) != 2 {
			t.Errorf("Expected vehicles to be split across 2 feeds, got %d: %v", len(byFeed), byFeed)
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

		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
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
		err := fetchAndStoreGTFSRTFeed(server, realtimeStore, client)
		if err == nil {
			t.Error("Expected error when accessing closed server, got nil")
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

	if err := fetchAndStoreGTFSRTFeed(first, store, client); err != nil {
		t.Fatalf("fetch first agency feed: %v", err)
	}
	firstData := store.Get(first.ServerKey())
	if err := fetchAndStoreGTFSRTFeed(second, store, client); err != nil {
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

	if err := fetchAndStoreGTFSRTFeed(first, store, client); err != nil {
		t.Fatalf("fetch first deployment feed: %v", err)
	}
	firstData := store.Get(first.ServerKey())
	if err := fetchAndStoreGTFSRTFeed(second, store, client); err != nil {
		t.Fatalf("fetch second deployment feed: %v", err)
	}
	if firstData == nil || store.Get(second.ServerKey()) == nil {
		t.Fatal("expected realtime data for both deployments sharing an agency_id")
	}
	if store.Get(first.ServerKey()) != firstData {
		t.Fatal("second deployment fetch replaced the first deployment's realtime data")
	}
}

func TestFetchAndStoreGTFSRTFeedKeepsSameIDAcrossFeeds(t *testing.T) {
	// Two feeds both report vehicle "101" but at different positions. GTFS-RT
	// vehicle IDs are only unique within a feed, so these are two distinct
	// physical vehicles and must BOTH be retained, each tagged with its feed.
	feedA := marshalVehicleFeed(t, "101", 47.60, -122.30)
	feedB := marshalVehicleFeed(t, "101", 47.65, -122.35)
	serverA := serveBytes(t, feedA)
	serverB := serveBytes(t, feedB)
	defer serverA.Close()
	defer serverB.Close()

	server := models.ObaServer{
		AgencyID: "agency-1",
		GtfsRTFeeds: []models.GtfsRTFeed{
			{VehiclePositionURL: serverA.URL},
			{VehiclePositionURL: serverB.URL},
		},
	}
	client := &http.Client{Timeout: 5 * time.Second}
	store := NewRealtimeStore()
	if err := fetchAndStoreGTFSRTFeed(server, store, client); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	realtimeData := store.Get(server.ServerKey())
	if realtimeData == nil {
		t.Fatal("expected realtime data")
	}
	if len(realtimeData.Vehicles) != 2 {
		t.Fatalf("expected both same-ID vehicles to be retained, got %d", len(realtimeData.Vehicles))
	}

	latByFeed := make(map[string]float32)
	for _, realtimeVehicle := range realtimeData.Vehicles {
		if realtimeVehicle.Vehicle.ID == nil || realtimeVehicle.Vehicle.ID.ID != "101" {
			t.Fatalf("unexpected vehicle descriptor: %+v", realtimeVehicle.Vehicle.ID)
		}
		if realtimeVehicle.Vehicle.Position == nil || realtimeVehicle.Vehicle.Position.Latitude == nil {
			t.Fatal("vehicle position missing")
		}
		latByFeed[realtimeVehicle.FeedID] = *realtimeVehicle.Vehicle.Position.Latitude
	}

	if latByFeed["0"] == latByFeed["1"] {
		t.Fatalf("expected vehicles tagged with distinct feed IDs and positions, got %v", latByFeed)
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

func TestFetchAndStoreGTFSRTFeedOnceSingleFetch(t *testing.T) {
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
	keys := []string{
		models.ServerKey(server.ObaBaseURL, "agency-A"),
		models.ServerKey(server.ObaBaseURL, "agency-B"),
		models.ServerKey(server.ObaBaseURL, "agency-C"),
	}

	if err := fetchAndStoreGTFSRTFeedOnce(server, keys, store, client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exactly one HTTP fetch must have been issued, regardless of how many
	// keys we registered the result under.
	if rt.calls != 1 {
		t.Fatalf("expected 1 RT fetch (server-mode share), got %d", rt.calls)
	}

	// All three serverKeys must yield the same *RealtimeData pointer and the
	// same single vehicle.
	first := store.Get(keys[0])
	second := store.Get(keys[1])
	third := store.Get(keys[2])
	if first == nil || second == nil || third == nil {
		t.Fatal("expected all three keys to be populated")
	}
	if first != second || second != third {
		t.Fatalf("expected pointer-sharing across serverKeys; got %p, %p, %p", first, second, third)
	}
	if got := len(first.Vehicles); got != 1 {
		t.Fatalf("expected 1 vehicle, got %d", got)
	}
}

func TestFetchAndStoreGTFSRTFeedOnceEmptyKeysIsNoop(t *testing.T) {
	// With no storeKeys the function should not issue any HTTP request — the
	// RT fetch is the expensive step and we want callers to skip it cleanly.
	rt := &countingRoundTripper{body: []byte{}}
	client := &http.Client{Transport: rt}

	server := models.ObaServer{
		ServerName: "noop",
		ObaBaseURL: "https://example.com",
		ObaApiKey:  "key",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: "https://rt.example.com/vehicles.pb",
		}},
	}
	store := NewRealtimeStore()

	if err := fetchAndStoreGTFSRTFeedOnce(server, nil, store, client); err != nil {
		t.Fatalf("expected nil error for empty storeKeys, got: %v", err)
	}
	if rt.calls != 0 {
		t.Fatalf("expected 0 fetches when no storeKeys, got %d", rt.calls)
	}
}

func TestFetchAndStoreGTFSRTFeedShimMatchesOnce(t *testing.T) {
	// The single-key shim must produce the same observable behavior as
	// fetchAndStoreGTFSRTFeedOnce with one key.
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

	if err := fetchAndStoreGTFSRTFeed(server, store, client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("expected 1 fetch via shim, got %d", rt.calls)
	}
	got := store.Get(server.ServerKey())
	if got == nil {
		t.Fatal("expected the shim to populate server.ServerKey()")
	}
	if len(got.Vehicles) != 1 {
		t.Fatalf("expected 1 vehicle from shim, got %d", len(got.Vehicles))
	}
}

// floatPtr returns a pointer to f. Used to build *float64 stop lat/lons for
// the synthetic bundles below without having to declare locals at every
// call site.
func floatPtr(f float64) *float64 { return &f }

// makeSyntheticBundle builds a *remoteGtfs.Static containing one agency row
// plus a list of stops. Tests use this to construct minimal bundles with
// the exact collision shape they need (same stop_id, different lat/lon; or
// same agency_id, different identity fields).
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

func TestMergeStaticStopsExactDuplicateSilentlyKept(t *testing.T) {
	// Two bundles declare stop_id="A" at the same lat/lon. Silent skip.
	lat, lon := 1.0, 2.0
	bundleA := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A", Latitude: floatPtr(lat), Longitude: floatPtr(lon)},
	})
	bundleB := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A", Latitude: floatPtr(lat), Longitude: floatPtr(lon)},
	})

	merged, _ := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundleA, bundleB})
	if got := len(merged.Stops); got != 1 {
		t.Fatalf("expected 1 stop after dedup, got %d", got)
	}
}

func TestMergeStaticStopsLocationCollisionKeptFirst(t *testing.T) {
	// Two bundles declare stop_id="A" at DIFFERENT lat/lon. The first wins
	// and the duplicate's location is dropped. The exact behavior (warn
	// vs. silent) is asserted in the Sentry-warn test below; here we just
	// verify the kept entry is the first occurrence.
	firstLat, firstLon := 1.0, 2.0
	dupLat, dupLon := 3.0, 4.0
	bundleA := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A", Latitude: floatPtr(firstLat), Longitude: floatPtr(firstLon)},
	})
	bundleB := makeSyntheticBundle(t, "agency-A", "Agency A", "https://a.example", []remoteGtfs.Stop{
		{Id: "A", Latitude: floatPtr(dupLat), Longitude: floatPtr(dupLon)},
	})

	merged, _ := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundleA, bundleB})
	if got := len(merged.Stops); got != 1 {
		t.Fatalf("expected 1 stop after dedup, got %d", got)
	}
	got := merged.Stops[0]
	if got.Latitude == nil || *got.Latitude != firstLat {
		t.Fatalf("expected first occurrence's lat to win; got %v", got.Latitude)
	}
	if got.Longitude == nil || *got.Longitude != firstLon {
		t.Fatalf("expected first occurrence's lon to win; got %v", got.Longitude)
	}
}

func TestSameStopLocation(t *testing.T) {
	lat, lon := 1.0, 2.0
	cases := []struct {
		name                   string
		lat1, lon1, lat2, lon2 *float64
		want                   bool
	}{
		{"both nil", nil, nil, nil, nil, true},
		{"same lat/lon", floatPtr(lat), floatPtr(lon), floatPtr(lat), floatPtr(lon), true},
		{"same values different ptrs", floatPtr(lat), floatPtr(lon), floatPtr(lat), floatPtr(lon), true},
		{"different lat", floatPtr(lat), floatPtr(lon), floatPtr(3.0), floatPtr(lon), false},
		{"different lon", floatPtr(lat), floatPtr(lon), floatPtr(lat), floatPtr(4.0), false},
		{"left lat nil", nil, floatPtr(lon), floatPtr(lat), floatPtr(lon), false},
		{"right lat nil", floatPtr(lat), floatPtr(lon), nil, floatPtr(lon), false},
		{"both nils with lon nil both sides", nil, nil, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameStopLocation(tc.lat1, tc.lon1, tc.lat2, tc.lon2); got != tc.want {
				t.Fatalf("sameStopLocation(%v,%v,%v,%v) = %v, want %v",
					tc.lat1, tc.lon1, tc.lat2, tc.lon2, got, tc.want)
			}
		})
	}
}

func TestMergeStaticAgenciesExactDuplicateSilentlyKept(t *testing.T) {
	// Two bundles declare agency_id="MTA" with identical (Name, Url). The
	// second is silently skipped.
	bundleA := makeSyntheticBundle(t, "MTA", "Metro Transit", "https://mta.example", nil)
	bundleB := makeSyntheticBundle(t, "MTA", "Metro Transit", "https://mta.example", nil)

	_, declared := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundleA, bundleB})
	if got := len(declared); got != 1 {
		t.Fatalf("expected 1 declared agency after dedup, got %d", got)
	}
	if declared[0].AgencyID != "MTA" {
		t.Fatalf("expected agency_id=MTA, got %q", declared[0].AgencyID)
	}
	if declared[0].AgencyName != "Metro Transit" {
		t.Fatalf("expected first occurrence's name to win, got %q", declared[0].AgencyName)
	}
}

func TestMergeStaticAgenciesNameCollisionKeptFirst(t *testing.T) {
	// Same agency_id, different Name. The first wins; second is dropped.
	bundleA := makeSyntheticBundle(t, "MTA", "Metro Transit", "https://mta.example", nil)
	bundleB := makeSyntheticBundle(t, "MTA", "Metro Authority", "https://mta.example", nil)

	_, declared := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundleA, bundleB})
	if got := len(declared); got != 1 {
		t.Fatalf("expected 1 declared agency, got %d", got)
	}
	if declared[0].AgencyName != "Metro Transit" {
		t.Fatalf("expected first occurrence's name to win, got %q", declared[0].AgencyName)
	}
}

func TestMergeStaticAgenciesURLCollisionKeptFirst(t *testing.T) {
	// Same agency_id and Name, different Url. The first wins.
	bundleA := makeSyntheticBundle(t, "MTA", "Metro", "https://mta-a.example", nil)
	bundleB := makeSyntheticBundle(t, "MTA", "Metro", "https://mta-b.example", nil)

	_, declared := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundleA, bundleB})
	if got := len(declared); got != 1 {
		t.Fatalf("expected 1 declared agency, got %d", got)
	}
}
