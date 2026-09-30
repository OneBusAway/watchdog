package metrics

import (
	"math"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestVehicleFreshnessTransitions(t *testing.T) {
	server := models.ObaServer{AgencyID: "fresh-agency", AgencyName: "Fresh Agency", ServerName: "fresh-server", ObaBaseURL: "https://freshness.example.com"}
	store := gtfs.NewRealtimeStore()
	lastSeen := NewVehicleLastSeen()
	lat, lon := float32(47), float32(-122)
	source := time.Unix(100, 0).UTC()
	observation := time.Unix(1000, 0).UTC()
	data := &models.RealtimeData{
		ObservationAt: observation,
		Feeds:         []models.RealtimeFeed{{FeedID: "0", FullDataset: true}},
		Vehicles: []models.RealtimeVehicle{{FeedID: "0", StateHash: "stationary", Vehicle: remoteGtfs.Vehicle{
			ID: &remoteGtfs.VehicleID{ID: "vehicle-1"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &source,
		}}},
	}
	store.Set(server.ServerKey(), data)

	track := func() {
		t.Helper()
		if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
			t.Fatal(err)
		}
	}
	track()
	track() // Identical repeat.

	counterLabels := map[string]string{"agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0"}
	if got, _ := getCounterValue(GtfsRtVehicleSourceTimestampAdvances, counterLabels); got != 0 {
		t.Fatalf("identical repeat advanced source counter: %v", got)
	}
	if got, _ := getCounterValue(GtfsRtVehicleStateChanges, counterLabels); got != 0 {
		t.Fatalf("identical repeat changed state counter: %v", got)
	}

	advanced := source.Add(30 * time.Second)
	data.ObservationAt = observation.Add(time.Minute)
	data.Vehicles[0].Vehicle.Timestamp = &advanced
	store.Set(server.ServerKey(), data)
	track()
	if got, _ := getCounterValue(GtfsRtVehicleSourceTimestampAdvances, counterLabels); got != 1 {
		t.Fatalf("timestamp advance counter = %v, want 1", got)
	}
	if got, _ := getCounterValue(GtfsRtVehicleStateChanges, counterLabels); got != 0 {
		t.Fatalf("stationary timestamp advance changed semantic state: %v", got)
	}

	data.ObservationAt = observation.Add(2 * time.Minute)
	data.Vehicles[0].StateHash = "moved"
	store.Set(server.ServerKey(), data)
	track()
	if got, _ := getCounterValue(GtfsRtVehicleStateChanges, counterLabels); got != 1 {
		t.Fatalf("state change counter = %v, want 1", got)
	}
	vehicleLabels := map[string]string{"vehicle_id": "vehicle-1", "agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0"}
	if got, _ := getMetricValue(GtfsRtVehicleStateLastChangedTimestamp, vehicleLabels); got != float64(data.ObservationAt.Unix()) {
		t.Fatalf("state last changed = %v, want %d", got, data.ObservationAt.Unix())
	}
}

func TestVehicleSpeedRetentionUsesDynamicGrace(t *testing.T) {
	server := models.ObaServer{AgencyID: "speed-grace", AgencyName: "Speed Grace", ServerName: "speed-grace", ObaBaseURL: "https://speed-grace.example.com"}
	store := gtfs.NewRealtimeStore()
	lastSeen := NewVehicleLastSeen()
	lastSeen.SetCollectionInterval(30 * time.Second)
	lat, lon := float32(47), float32(-122)
	source := time.Unix(100, 0).UTC()
	observation := time.Unix(1000, 0).UTC()
	data := &models.RealtimeData{ObservationAt: observation, Vehicles: []models.RealtimeVehicle{{
		FeedID: "0", StateHash: "first", Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &source},
	}}}
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}

	advanced := source.Add(30 * time.Second)
	movedLon := float32(-121.999)
	data.ObservationAt = observation.Add(30 * time.Second)
	data.Vehicles[0].StateHash = "second"
	data.Vehicles[0].Vehicle.Timestamp = &advanced
	data.Vehicles[0].Vehicle.Position.Longitude = &movedLon
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}

	labels := map[string]string{"vehicle_id": "v", "agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0"}
	computedAt, err := getMetricValue(GtfsRtVehicleSpeedLastComputedTimestamp, labels)
	if err != nil || computedAt != float64(data.ObservationAt.Unix()) {
		t.Fatalf("speed computed timestamp = %v, %v", computedAt, err)
	}

	// The source repeats the exact timestamp and state. The speed remains valid
	// inside max(3*30s collection, 2*30s source) = 90s.
	data.ObservationAt = observation.Add(2 * time.Minute)
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}
	if got := seriesMatching(VehicleSpeedGauge, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 1 {
		t.Fatalf("speed series inside grace = %d, want 1", len(got))
	}

	data.ObservationAt = observation.Add(2*time.Minute + time.Second)
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}
	if got := seriesMatching(VehicleSpeedGauge, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("speed series after grace = %d, want 0", len(got))
	}
	if got := seriesMatching(GtfsRtVehicleSpeedLastComputedTimestamp, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("speed timestamp series after grace = %d, want 0", len(got))
	}
}

func TestVehicleSpeedGraceBounds(t *testing.T) {
	if got := vehicleSpeedGrace(5*time.Second, 10*time.Second); got != time.Minute {
		t.Fatalf("minimum grace = %s", got)
	}
	if got := vehicleSpeedGrace(30*time.Second, 2*time.Minute); got != 4*time.Minute {
		t.Fatalf("source-derived grace = %s", got)
	}
	if got := vehicleSpeedGrace(2*time.Minute, 10*time.Minute); got != 5*time.Minute {
		t.Fatalf("maximum grace = %s", got)
	}
}

func TestVehicleLatLonRejectsNonFiniteCoordinates(t *testing.T) {
	nan := float32(math.NaN())
	lon := float32(1)
	if _, _, valid := vehicleLatLon(remoteGtfs.Vehicle{Position: &remoteGtfs.Position{Latitude: &nan, Longitude: &lon}}); valid {
		t.Fatal("NaN position was accepted")
	}
}

func TestServerFullDatasetRemovesVehiclesFromNoLongerLiveAgency(t *testing.T) {
	server := models.ObaServer{ServerName: "server", ObaBaseURL: "https://full-dataset.example"}
	agencyA := models.ObaServer{ServerName: "server", ObaBaseURL: server.ObaBaseURL, AgencyID: "a"}
	agencyB := models.ObaServer{ServerName: "server", ObaBaseURL: server.ObaBaseURL, AgencyID: "b"}
	store := NewVehicleLastSeen()
	store.Set(agencyA.ServerKey(), "0", "a-vehicle", LastSeen{VehicleID: "a-vehicle", FeedID: "0"})
	store.Set(agencyB.ServerKey(), "0", "b-vehicle", LastSeen{VehicleID: "b-vehicle", FeedID: "0"})

	cleanupFullDatasetVehicles(server, []models.ObaServer{agencyA}, store, &models.RealtimeData{Feeds: []models.RealtimeFeed{{FeedID: "0", FullDataset: true}}}, map[string]map[string]bool{
		agencyA.ServerKey() + "|0": {"a-vehicle": true},
	})

	if _, ok := store.Get(agencyA.ServerKey(), "0", "a-vehicle"); !ok {
		t.Fatal("seen vehicle was removed")
	}
	if _, ok := store.Get(agencyB.ServerKey(), "0", "b-vehicle"); ok {
		t.Fatal("vehicle from no-longer-live agency was retained")
	}
}

func TestMissingVehicleTimestampIsNotSynthesized(t *testing.T) {
	server := models.ObaServer{AgencyID: "missing-ts", AgencyName: "Missing TS", ServerName: "missing-ts", ObaBaseURL: "https://missing-ts.example.com"}
	lat, lon := float32(1), float32(2)
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{ObservationAt: time.Unix(2000, 0), Vehicles: []models.RealtimeVehicle{{
		FeedID: "0", StateHash: "state", Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}},
	}}})
	lastSeen := NewVehicleLastSeen()
	vehicleLabels := []string{"v", server.AgencyID, server.AgencyName, server.ServerName, server.ObaBaseURL, "0"}
	VehicleSpeedGauge.WithLabelValues(vehicleLabels...).Set(12)
	VehicleSpeedDiscrepancyRatioGauge.WithLabelValues(vehicleLabels...).Set(0.5)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}
	if got := seriesMatching(GtfsRtVehicleSourceTimestamp, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("missing timestamp emitted a source timestamp series: %d", len(got))
	}
	state, ok := lastSeen.Get(server.ServerKey(), "0", "v")
	if !ok || !state.Time.IsZero() {
		t.Fatalf("missing source timestamp was replaced: %+v", state)
	}
	if got := seriesMatching(VehicleSpeedGauge, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("uncomputable speed retained an old series: %d", len(got))
	}
	if got := seriesMatching(VehicleSpeedDiscrepancyRatioGauge, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("uncomputable discrepancy retained an old series: %d", len(got))
	}
}

func TestFeedFreshnessMultipleFeedsAndFailureGap(t *testing.T) {
	server := models.ObaServer{AgencyID: "feed-agency", AgencyName: "Feed Agency", ServerName: "feed-server", ObaBaseURL: "https://feeds.example.com"}
	store := NewFeedFreshnessStore()
	now := time.Unix(3000, 0).UTC()
	source := time.Unix(2500, 0).UTC()
	for _, feed := range []struct{ id, url, hash string }{{"0", "https://feeds.example.com/a", "a"}, {"1", "https://feeds.example.com/b", "b"}} {
		store.Observe(server, gtfs.FeedObservation{FeedID: feed.id, FeedURL: feed.url, Success: true, ObservedAt: now, SourceTimestamp: &source, PayloadHash: feed.hash, VehicleEntities: 2, VehicleTimestampMissing: 1})
	}
	if got := len(seriesMatching(GtfsRtFeedVehicleEntities, map[string]string{"server_url": server.ObaBaseURL})); got != 2 {
		t.Fatalf("expected one current series per feed, got %d", got)
	}
	feedZeroLabels := map[string]string{"agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0", "feed_url": "https://feeds.example.com/a"}
	store.Observe(server, gtfs.FeedObservation{FeedID: "0", FeedURL: "https://feeds.example.com/a", Success: true, ObservedAt: now.Add(time.Minute), PayloadHash: "a"})
	if got, _ := getMetricValue(GtfsRtFeedPayloadLastChangedTimestamp, feedZeroLabels); got != float64(now.Unix()) {
		t.Fatalf("identical payload moved last-changed time: %v", got)
	}
	store.Observe(server, gtfs.FeedObservation{FeedID: "0", FeedURL: "https://feeds.example.com/a", Success: true, ObservedAt: now.Add(2 * time.Minute), PayloadHash: "changed"})
	if got, _ := getMetricValue(GtfsRtFeedPayloadLastChangedTimestamp, feedZeroLabels); got != float64(now.Add(2*time.Minute).Unix()) {
		t.Fatalf("changed payload did not move last-changed time: %v", got)
	}

	failedLabels := map[string]string{"agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "1", "feed_url": "https://feeds.example.com/b"}
	store.Observe(server, gtfs.FeedObservation{FeedID: "1", FeedURL: "https://feeds.example.com/b", ObservedAt: now.Add(time.Minute)})
	if got, _ := getMetricValue(GtfsRtFeedFetchSuccess, failedLabels); got != 0 {
		t.Fatalf("failed fetch success gauge = %v, want 0", got)
	}
	if got := seriesMatching(GtfsRtFeedVehicleEntities, map[string]string{"server_url": server.ObaBaseURL, "feed": "1"}); len(got) != 0 {
		t.Fatalf("failed feed retained/emitted current entity value: %d series", len(got))
	}
	if got := seriesMatching(GtfsRtFeedVehicleEntities, map[string]string{"server_url": server.ObaBaseURL, "feed": "0"}); len(got) != 1 {
		t.Fatalf("independent successful feed was disturbed: %d series", len(got))
	}
}

func TestFeedFreshnessReconcileRetiresRemovedFeed(t *testing.T) {
	server := models.ObaServer{
		AgencyID: "reconcile-agency", AgencyName: "Reconcile Agency", ServerName: "reconcile-server",
		ObaBaseURL:  "https://reconcile.example.com",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "https://reconcile.example.com/a"}, {VehiclePositionURL: "https://reconcile.example.com/b"}},
	}
	store := NewFeedFreshnessStore()
	vehicles := NewVehicleLastSeen()
	now := time.Unix(4000, 0).UTC()
	store.Observe(server, gtfs.FeedObservation{FeedID: "0", FeedURL: server.GtfsRTFeeds[0].VehiclePositionURL, Success: true, ObservedAt: now, PayloadHash: "a"})
	store.Observe(server, gtfs.FeedObservation{FeedID: "1", FeedURL: server.GtfsRTFeeds[1].VehiclePositionURL, Success: true, ObservedAt: now, PayloadHash: "b"})
	vehicles.Set(server.ServerKey(), "1", "vehicle-1", LastSeen{
		VehicleID: "vehicle-1", FeedID: "1", AgencyID: server.AgencyID, AgencyName: server.AgencyName,
		ServerName: server.ServerName, ServerURL: server.ObaBaseURL,
	})

	server.GtfsRTFeeds = server.GtfsRTFeeds[:1]
	vehicles.RemoveFeeds(server, store.Reconcile(server))

	if got := seriesMatching(GtfsRtFeedFetchSuccess, map[string]string{"server_url": server.ObaBaseURL, "feed": "1"}); len(got) != 0 {
		t.Fatalf("removed feed retained fetch series: %d", len(got))
	}
	if got := seriesMatching(GtfsRtFeedFetchSuccess, map[string]string{"server_url": server.ObaBaseURL, "feed": "0"}); len(got) != 1 {
		t.Fatalf("retained feed was disturbed: %d series", len(got))
	}
	if vehicles.Count(server.ServerKey()) != 0 {
		t.Fatal("removed feed's vehicle history remained")
	}
}

func TestFullDatasetAndExpiryDeleteVehicleSeries(t *testing.T) {
	server := models.ObaServer{AgencyID: "cleanup", AgencyName: "Cleanup", ServerName: "cleanup", ObaBaseURL: "https://cleanup.example.com"}
	lat, lon := float32(1), float32(2)
	source := time.Unix(100, 0).UTC()
	store := gtfs.NewRealtimeStore()
	lastSeen := NewVehicleLastSeen()
	data := &models.RealtimeData{ObservationAt: time.Now().UTC(), Feeds: []models.RealtimeFeed{{FeedID: "0", FullDataset: true}}}
	for _, id := range []string{"keep", "leave"} {
		data.Vehicles = append(data.Vehicles, models.RealtimeVehicle{FeedID: "0", StateHash: id, Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: id}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &source}})
	}
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}
	data.Vehicles = data.Vehicles[:1]
	store.Set(server.ServerKey(), data)
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := lastSeen.Get(server.ServerKey(), "0", "leave"); ok {
		t.Fatal("vehicle absent from FULL_DATASET snapshot remained in state")
	}
	if got := seriesMatching(GtfsRtVehicleStateLastChangedTimestamp, map[string]string{"server_url": server.ObaBaseURL, "vehicle_id": "leave"}); len(got) != 0 {
		t.Fatalf("departed vehicle Prometheus series remained: %d", len(got))
	}

	kept, _ := lastSeen.Get(server.ServerKey(), "0", "keep")
	kept.ObservedAt = time.Now().Add(-2 * time.Hour)
	lastSeen.Set(server.ServerKey(), "0", "keep", kept)
	lastSeen.clear(time.Hour)
	if got := lastSeen.Count(server.ServerKey()); got != 0 {
		t.Fatalf("expired vehicle remained in state: %d", got)
	}
	if got := seriesMatching(GtfsRtVehicleStateLastChangedTimestamp, map[string]string{"server_url": server.ObaBaseURL, "vehicle_id": "keep"}); len(got) != 0 {
		t.Fatalf("expired vehicle Prometheus series remained: %d", len(got))
	}
}
