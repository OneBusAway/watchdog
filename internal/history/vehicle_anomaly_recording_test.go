package history

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

var (
	vehicleT0      = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	vehicleFeedURL = "https://vehicles.example/vehicle-positions"
	vehicleServer  = models.ObaServer{
		ServerName: "vehicle-server",
		ObaBaseURL: "https://vehicles.example",
		AgencyID:   "a",
		AgencyName: "Agency A",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: vehicleFeedURL,
		}},
	}
)

// testVehicle is a fresh, valid vehicle in transit on trip-1.
func testVehicle(id string, at time.Time) remoteGtfs.Vehicle {
	lat, lon := float32(47.6), float32(-122.3)
	sourceTime := at.Add(-10 * time.Second)
	status := gtfsrt.VehiclePosition_IN_TRANSIT_TO
	return remoteGtfs.Vehicle{
		ID:                &remoteGtfs.VehicleID{ID: id},
		Position:          &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
		Timestamp:         &sourceTime,
		CurrentStatus:     &status,
		Trip:              &remoteGtfs.Trip{ID: remoteGtfs.TripID{ID: "trip-1", RouteID: "route-1"}},
		IsEntityInMessage: true,
	}
}

func zeroCoordinates(v remoteGtfs.Vehicle) remoteGtfs.Vehicle {
	zero := float32(0)
	v.Position = &remoteGtfs.Position{Latitude: &zero, Longitude: &zero}
	return v
}

func onTrip(v remoteGtfs.Vehicle, tripID string) remoteGtfs.Vehicle {
	v.Trip = &remoteGtfs.Trip{ID: remoteGtfs.TripID{ID: tripID, RouteID: "route-1"}}
	return v
}

// vehicleSnapshot is a full-dataset snapshot of vehicleServer's feed.
func vehicleSnapshot(at time.Time, vehicles ...remoteGtfs.Vehicle) *models.RealtimeData {
	return filteredSnapshot(at, nil, vehicles...)
}

// filteredSnapshot is a full-dataset snapshot whose response also held the
// vehicles in alsoReported, which agency-mode filtering left out of this
// entry's view.
func filteredSnapshot(at time.Time, alsoReported []string, vehicles ...remoteGtfs.Vehicle) *models.RealtimeData {
	reported := make(map[string]struct{}, len(vehicles)+len(alsoReported))
	for _, id := range alsoReported {
		reported[id] = struct{}{}
	}
	snapshot := &models.RealtimeData{ObservationAt: at}
	for _, v := range vehicles {
		reported[v.GetID().ID] = struct{}{}
		snapshot.Vehicles = append(snapshot.Vehicles, models.RealtimeVehicle{Vehicle: v, FeedID: "0"})
	}
	snapshot.Feeds = []models.RealtimeFeed{{FeedID: "0", FeedURL: vehicleFeedURL, FullDataset: true, ReportedVehicleIDs: reported}}
	return snapshot
}

// noWriteFailures fails the test when the service logged a failed history
// write.
func noWriteFailures(t *testing.T, logs fmt.Stringer) {
	t.Helper()
	if strings.Contains(logs.String(), "Failed to write investigation history") {
		t.Fatalf("a history write failed:\n%s", logs)
	}
}

func testBoxes(server models.ObaServer) *geo.BoundingBoxStore {
	boxes := geo.NewBoundingBoxStore()
	boxes.Set(server.ServerKey(), geo.BoundingBox{MinLat: 47, MaxLat: 48, MinLon: -123, MaxLon: -122})
	return boxes
}

func recordVehicles(h *HistoryService, c context.Context, server models.ObaServer, snapshot *models.RealtimeData) {
	h.RecordVehiclePositions(c, server, snapshot, metrics.NewVehicleAttributor(server, nil, nil), testBoxes(server), nil)
}

type storedVehicleEpisode struct {
	anomaly     string
	closeReason *string
	links       map[string]int
}

func storedVehicleEpisodes(t *testing.T, pool *pgxpool.Pool) []storedVehicleEpisode {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT e.vehicle_anomaly_episode_id, t.anomaly_code, e.close_reason
		FROM vehicle_anomaly_episode e
		JOIN vehicle_anomaly_type t ON t.anomaly_type_id = e.anomaly_type_id
		ORDER BY e.vehicle_anomaly_episode_id`)
	if err != nil {
		t.Fatalf("query episodes: %v", err)
	}
	defer rows.Close()
	var ids []int64
	var episodes []storedVehicleEpisode
	for rows.Next() {
		var id int64
		var e storedVehicleEpisode
		if err := rows.Scan(&id, &e.anomaly, &e.closeReason); err != nil {
			t.Fatalf("scan episode: %v", err)
		}
		ids = append(ids, id)
		episodes = append(episodes, e)
	}
	rows.Close()
	for i, id := range ids {
		episodes[i].links = make(map[string]int)
		linkRows, err := pool.Query(ctx, `SELECT evidence_role FROM vehicle_anomaly_episode_evidence WHERE vehicle_anomaly_episode_id = $1`, id)
		if err != nil {
			t.Fatalf("query links: %v", err)
		}
		for linkRows.Next() {
			var role string
			if err := linkRows.Scan(&role); err != nil {
				t.Fatalf("scan link: %v", err)
			}
			episodes[i].links[role]++
		}
		linkRows.Close()
	}
	return episodes
}

func onlyVehicleEpisode(t *testing.T, pool *pgxpool.Pool) storedVehicleEpisode {
	t.Helper()
	episodes := storedVehicleEpisodes(t, pool)
	if len(episodes) != 1 {
		t.Fatalf("got %d episodes, want 1: %+v", len(episodes), episodes)
	}
	return episodes[0]
}

func TestRecordVehiclePositions_NilPoolNoop(t *testing.T) {
	h := NewHistoryService(nil, discardLogger(), "test-build", nil)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))
}

func TestRecordVehiclePositions_OpensAndCloses(t *testing.T) {
	h, pool, _ := newCountedHistory(t)

	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))

	episode := onlyVehicleEpisode(t, pool)
	if episode.anomaly != string(anomalyInvalidCoordinate) || episode.closeReason != nil || episode.links[evidenceOpening] != 1 {
		t.Fatalf("after the open: %+v", episode)
	}
	var valid bool
	var reason, agency *string
	if err := pool.QueryRow(ctx, `
		SELECT pe.position_valid, pe.position_validation_reason, a.external_agency_id
		FROM vehicle_position_evidence pe LEFT JOIN agency a ON a.agency_id = pe.agency_id`).Scan(&valid, &reason, &agency); err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if valid || reason == nil || *reason != string(geo.PositionPlaceholderZeroCoordinate) || agency == nil || *agency != "a" {
		t.Fatalf("evidence valid=%v reason=%v agency=%v", valid, reason, agency)
	}

	later := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(later, testVehicle("bus-1", later)))

	episode = onlyVehicleEpisode(t, pool)
	if episode.closeReason == nil || *episode.closeReason != string(closeCoordinatesValidAgain) || episode.links[evidenceResolution] != 1 {
		t.Fatalf("after the close: %+v", episode)
	}
	if n := count(t, pool, "vehicle_position_response"); n != 2 {
		t.Errorf("got %d responses, want one per tick that changed something", n)
	}
}

// One evidence row per vehicle per response is shared by every episode the
// tick opens for it.
func TestRecordVehiclePositions_SharesEvidenceAcrossEpisodes(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	v := zeroCoordinates(testVehicle("bus-1", vehicleT0))
	stale := vehicleT0.Add(-time.Hour)
	v.Timestamp = &stale

	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, v))

	if n := len(storedVehicleEpisodes(t, pool)); n != 2 {
		t.Fatalf("got %d episodes, want stale and invalid", n)
	}
	if n := count(t, pool, "vehicle_position_evidence"); n != 1 {
		t.Fatalf("got %d evidence rows, want 1", n)
	}
}

func TestRecordVehiclePositions_NotPresentInFullDatasetCloses(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))

	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0.Add(30*time.Second)))

	episode := onlyVehicleEpisode(t, pool)
	if episode.closeReason == nil || *episode.closeReason != string(closeNotPresentInFull) {
		t.Fatalf("close_reason = %v, want not_present_in_full_dataset", episode.closeReason)
	}
	if episode.links[evidenceResolution] != 0 {
		t.Fatalf("an absent vehicle got resolution evidence: %+v", episode)
	}
}

func TestVehicle_QuietTickNoDatabase(t *testing.T) {
	h, _, counter := newCountedHistory(t)
	invalid := zeroCoordinates(testVehicle("bus-1", vehicleT0))
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, invalid))
	if episode := onlyVehicleEpisode(t, h.pool); episode.anomaly != string(anomalyInvalidCoordinate) {
		t.Fatalf("first tick opened %+v, want invalid_coordinate", episode)
	}
	counter.reset()

	later := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(later, zeroCoordinates(testVehicle("bus-1", later)), testVehicle("bus-2", later)))

	if n := counter.count(""); n != 0 {
		t.Fatalf("quiet tick ran %d statements, want 0", n)
	}
}

func TestVehicle_MeaningfulChangeSurvivesRestart(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))

	t1 := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(t1, onTrip(zeroCoordinates(testVehicle("bus-1", t1)), "trip-2")))
	if links := onlyVehicleEpisode(t, pool).links; links[evidenceMeaningfulChange] != 1 {
		t.Fatalf("links after the trip change = %v", links)
	}

	// A restart loads the trip of the newest evidence, so the same trip is
	// not a change.
	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	t2 := vehicleT0.Add(time.Minute)
	recordVehicles(restarted, ctx, vehicleServer, vehicleSnapshot(t2, onTrip(zeroCoordinates(testVehicle("bus-1", t2)), "trip-2")))
	if links := onlyVehicleEpisode(t, pool).links; links[evidenceMeaningfulChange] != 1 {
		t.Fatalf("links after the restart = %v, want no new change", links)
	}
}

func TestVehicle_FailedWriteRecordedNextTick(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, testVehicle("bus-1", vehicleT0)))
	t1 := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, canceled, vehicleServer, vehicleSnapshot(t1, zeroCoordinates(testVehicle("bus-1", t1))))
	if n := count(t, pool, "vehicle_anomaly_episode"); n != 0 {
		t.Fatalf("failed write left %d episodes", n)
	}

	t2 := vehicleT0.Add(time.Minute)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(t2, zeroCoordinates(testVehicle("bus-1", t2))))
	var openedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT r.observed_at FROM vehicle_anomaly_episode e
		JOIN vehicle_position_response r ON r.vehicle_position_response_id = e.opening_response_id`).Scan(&openedAt); err != nil {
		t.Fatalf("read opening: %v", err)
	}
	if !openedAt.Equal(t2) {
		t.Fatalf("opened at %v, want the next tick's time %v", openedAt, t2)
	}

	t3 := vehicleT0.Add(90 * time.Second)
	recordVehicles(h, canceled, vehicleServer, vehicleSnapshot(t3, testVehicle("bus-1", t3)))
	if onlyVehicleEpisode(t, pool).closeReason != nil {
		t.Fatal("failed write closed the episode")
	}
	t4 := vehicleT0.Add(2 * time.Minute)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(t4, testVehicle("bus-1", t4)))
	if onlyVehicleEpisode(t, pool).closeReason == nil {
		t.Fatal("the next tick did not close the episode")
	}
}

func TestRuleVersion_CreatedOnceAndOnChange(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	h.SetFetchInterval(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))
	if n := count(t, pool, "vehicle_anomaly_rule_version"); n != len(vehicleAnomalyTypes) {
		t.Fatalf("got %d rule versions, want one per type", n)
	}
	var name string
	var seconds *int32
	if err := pool.QueryRow(ctx, `
		SELECT r.version_name, r.stale_age_threshold_seconds FROM vehicle_anomaly_rule_version r
		JOIN vehicle_anomaly_type t ON t.anomaly_type_id = r.anomaly_type_id
		WHERE t.anomaly_code = 'stale_source_timestamp'`).Scan(&name, &seconds); err != nil {
		t.Fatalf("read stale rule: %v", err)
	}
	if name != "logic 1, stale > 150s" || seconds == nil || *seconds != 150 {
		t.Fatalf("stale rule = %q, %v", name, seconds)
	}

	// The same rules after a restart reuse the stored versions.
	same := NewHistoryService(pool, discardLogger(), "test-build", nil)
	same.SetFetchInterval(30 * time.Second)
	recordVehicles(same, ctx, vehicleServer, vehicleSnapshot(vehicleT0.Add(time.Minute), zeroCoordinates(testVehicle("bus-2", vehicleT0))))
	if n := count(t, pool, "vehicle_anomaly_rule_version"); n != len(vehicleAnomalyTypes) {
		t.Fatalf("got %d rule versions after a restart with the same rules", n)
	}

	// A new fetch interval changes only the stale threshold.
	changed := NewHistoryService(pool, discardLogger(), "test-build", nil)
	changed.startedAt = vehicleT0.Add(time.Hour)
	changed.SetFetchInterval(10 * time.Second)
	recordVehicles(changed, ctx, vehicleServer, vehicleSnapshot(vehicleT0.Add(2*time.Minute), zeroCoordinates(testVehicle("bus-3", vehicleT0))))
	if n := count(t, pool, "vehicle_anomaly_rule_version"); n != len(vehicleAnomalyTypes)+1 {
		t.Fatalf("got %d rule versions, want one more for the stale type", n)
	}
}

// Agency-mode entries on one base URL can share a feed URL, each with a
// snapshot filtered to its own agency. A vehicle that moves from agency a's
// trips to agency b's keeps its one open episode: b continues it even when b
// runs first, a's filtered view does not close it, and b closes it when the
// vehicle recovers. The same holds after a restart.
func TestRecordVehiclePositions_HandoffBetweenAgenciesSharingAFeed(t *testing.T) {
	logger, logs := bufferLogger()
	h, pool, _ := newCountedHistory(t)
	h.logger = logger
	agencyB := vehicleServer
	agencyB.AgencyID, agencyB.AgencyName = "b", "Agency B"

	// b has already loaded its memory when a opens the episode.
	recordVehicles(h, ctx, agencyB, filteredSnapshot(vehicleT0.Add(-30*time.Second), nil))
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))

	t1 := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, ctx, agencyB, filteredSnapshot(t1, nil, zeroCoordinates(testVehicle("bus-1", t1))))
	recordVehicles(h, ctx, vehicleServer, filteredSnapshot(t1, []string{"bus-1"}))
	if episode := onlyVehicleEpisode(t, pool); episode.closeReason != nil {
		t.Fatalf("the handoff closed the episode: %+v", episode)
	}

	restarted := NewHistoryService(pool, logger, "test-build", nil)
	t2 := vehicleT0.Add(time.Minute)
	recordVehicles(restarted, ctx, vehicleServer, filteredSnapshot(t2, []string{"bus-1"}))
	recordVehicles(restarted, ctx, agencyB, filteredSnapshot(t2, nil, testVehicle("bus-1", t2)))

	episode := onlyVehicleEpisode(t, pool)
	if episode.closeReason == nil || *episode.closeReason != string(closeCoordinatesValidAgain) {
		t.Fatalf("close_reason = %v, want coordinates_valid_again from agency b", episode.closeReason)
	}
	noWriteFailures(t, logs)
}

// A server-scoped entry and an agency-mode entry on one base URL see the
// same vehicle. The first to record a change records it once; the second
// finds nothing left to do.
func TestRecordVehiclePositions_ServerAndAgencyEntriesShareEpisodes(t *testing.T) {
	logger, logs := bufferLogger()
	h, pool, _ := newCountedHistory(t)
	h.logger = logger
	serverScoped := vehicleServer
	serverScoped.AgencyID, serverScoped.AgencyName = "", ""

	recordVehicles(h, ctx, serverScoped, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))
	t1 := vehicleT0.Add(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(t1, testVehicle("bus-1", t1)))
	recordVehicles(h, ctx, serverScoped, vehicleSnapshot(t1, testVehicle("bus-1", t1)))

	episode := onlyVehicleEpisode(t, pool)
	if episode.closeReason == nil || episode.links[evidenceResolution] != 1 {
		t.Fatalf("episode = %+v, want one close with one resolution", episode)
	}
	noWriteFailures(t, logs)
}

// The rule versions are recorded at the first recording after startup, even
// when that run only closes episodes.
func TestRuleVersion_RecordedWhenARunOnlyCloses(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	h.SetFetchInterval(30 * time.Second)
	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0))))

	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	restarted.startedAt = vehicleT0.Add(time.Hour)
	restarted.SetFetchInterval(10 * time.Second)
	t1 := vehicleT0.Add(30 * time.Second)
	recordVehicles(restarted, ctx, vehicleServer, vehicleSnapshot(t1, testVehicle("bus-1", t1)))

	if onlyVehicleEpisode(t, pool).closeReason == nil {
		t.Fatal("the run did not close the episode")
	}
	if n := count(t, pool, "vehicle_anomaly_rule_version"); n != len(vehicleAnomalyTypes)+1 {
		t.Fatalf("got %d rule versions, want the changed stale rule recorded", n)
	}
}

// A vehicle known only from a trip update has no position entity, so it is
// not judged.
func TestRecordVehiclePositions_SkipsTripUpdateOnlyVehicles(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	v := testVehicle("bus-1", vehicleT0)
	v.Position = nil
	v.IsEntityInMessage = false

	recordVehicles(h, ctx, vehicleServer, vehicleSnapshot(vehicleT0, v))

	if n := count(t, pool, "vehicle_anomaly_episode"); n != 0 {
		t.Fatalf("got %d episodes for a trip-update-only vehicle", n)
	}
}

// Evidence records the speed the telemetry pass computed for the vehicle
// this tick, and null when it computed none.
func TestRecordVehiclePositions_EvidenceCarriesComputedSpeed(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	lastSeen := metrics.NewVehicleLastSeen()
	speed, ratio := 12.5, 0.25
	lastSeen.Set(vehicleServer.ServerKey(), "0", "bus-1", metrics.LastSeen{
		SpeedLastComputed:     vehicleT0,
		ComputedSpeedMps:      &speed,
		SpeedDiscrepancyRatio: &ratio,
	})
	attributor := metrics.NewVehicleAttributor(vehicleServer, nil, nil)
	snapshot := vehicleSnapshot(vehicleT0, zeroCoordinates(testVehicle("bus-1", vehicleT0)), zeroCoordinates(testVehicle("bus-2", vehicleT0)))

	h.RecordVehiclePositions(ctx, vehicleServer, snapshot, attributor, testBoxes(vehicleServer), lastSeen)

	rows, err := pool.Query(ctx, `
		SELECT v.external_vehicle_id, pe.computed_speed_mps, pe.speed_discrepancy_ratio
		FROM vehicle_position_evidence pe
		JOIN vehicle_entity v ON v.vehicle_entity_id = pe.vehicle_entity_id
		ORDER BY v.external_vehicle_id`)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	defer rows.Close()
	got := map[string][2]*float64{}
	for rows.Next() {
		var id string
		var computed, discrepancy *float64
		if err := rows.Scan(&id, &computed, &discrepancy); err != nil {
			t.Fatalf("scan evidence: %v", err)
		}
		got[id] = [2]*float64{computed, discrepancy}
	}
	if s := got["bus-1"]; s[0] == nil || *s[0] != speed || s[1] == nil || *s[1] != ratio {
		t.Errorf("bus-1 speed, ratio = %v, %v; want %v, %v", s[0], s[1], speed, ratio)
	}
	if s := got["bus-2"]; s[0] != nil || s[1] != nil {
		t.Errorf("bus-2 has no computed speed but got %v, %v", s[0], s[1])
	}
}
