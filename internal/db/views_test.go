package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// The tests below check the episode views from migration 00002 against the
// rules in the "Views for the investigation API" section of
// docs/historical-investigation-schema.md (D28, D29). They read each view into
// its generated dbgen struct, so a column sqlc typed as non-null but which the
// view can return as null fails here.

// base is the fixed instant the view tests count minutes from.
var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func at(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }

// open marks an episode with no closing response.
const open = -1

// readView returns the one row of a view whose idColumn equals id.
func readView[T any](t *testing.T, f fixture, view, idColumn string, id int64) T {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT * FROM "+view+" WHERE "+idColumn+" = $1", id)
	if err != nil {
		t.Fatalf("query %s: %v", view, err)
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	if err != nil {
		t.Fatalf("read %s %d: %v", view, id, err)
	}
	return row
}

// show prints a nullable value for a failure message: the value, or "null".
func show[T any](p *T) any {
	if p == nil {
		return "null"
	}
	return *p
}

func expectTime(t *testing.T, name string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func expectNullTime(t *testing.T, name string, got *time.Time) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %v, want null", name, *got)
	}
}

// unmatchedViewFixture is one agency on one server, with helpers that write
// complete OBA responses at chosen times and episodes opened and closed by them.
type unmatchedViewFixture struct {
	fixture
	server, agency int64
}

func newUnmatchedViewFixture(t *testing.T) unmatchedViewFixture {
	f := fixture{t, dbtest.NewMigratedPool(t)}
	server := f.id("INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n') RETURNING server_id")
	agency := f.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'A') RETURNING agency_id", server)
	return unmatchedViewFixture{f, server, agency}
}

// response writes a complete response observed at the given minute and returns
// its server-wide response_id and per-agency response_agency_id.
func (u unmatchedViewFixture) response(minute int) (responseID, responseAgencyID int64) {
	responseID = u.id(`INSERT INTO onebusaway_metrics_response (server_id, observed_at)
		VALUES ($1, $2) RETURNING response_id`, u.server, at(minute))
	responseAgencyID = u.id(`INSERT INTO onebusaway_metrics_response_agency (response_id, agency_id, response_complete)
		VALUES ($1, $2, true) RETURNING response_agency_id`, responseID, u.agency)
	return responseID, responseAgencyID
}

// unmatchedKind names the tables of one unmatched episode type, so the stop
// and trip tests share their setup.
type unmatchedKind struct {
	identityTable, identityIDColumn, externalIDColumn string
	episodeTable, episodeIDColumn, view               string
}

var (
	stopKind = unmatchedKind{
		"reported_stop", "reported_stop_id", "oba_stop_id",
		"unmatched_stop_episode", "unmatched_stop_episode_id", "unmatched_stop_episode_view",
	}
	tripKind = unmatchedKind{
		"reported_trip", "reported_trip_id", "oba_trip_id",
		"unmatched_trip_episode", "unmatched_trip_episode_id", "unmatched_trip_episode_view",
	}
)

func (u unmatchedViewFixture) identity(k unmatchedKind, externalID string) int64 {
	return u.id("INSERT INTO "+k.identityTable+" (agency_id, "+k.externalIDColumn+") VALUES ($1, $2) RETURNING "+k.identityIDColumn,
		u.agency, externalID)
}

// episode opens an episode at openedMinute and, unless closedMinute is open,
// closes it at closedMinute.
func (u unmatchedViewFixture) episode(k unmatchedKind, identity int64, openedMinute, closedMinute int) int64 {
	_, opening := u.response(openedMinute)
	var closing *int64
	var reason *string
	lastReported := at(openedMinute)
	if closedMinute != open {
		_, c := u.response(closedMinute)
		closing = &c
		r := "not_reported"
		reason = &r
		lastReported = at(closedMinute - 1)
	}
	return u.id("INSERT INTO "+k.episodeTable+` (`+k.identityIDColumn+`, agency_id, first_response_agency_id,
		closing_response_agency_id, last_reported_at, close_reason)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+k.episodeIDColumn,
		identity, u.agency, opening, closing, lastReported, reason)
}

func (u unmatchedViewFixture) staticVersion(publishedMinute int) int64 {
	return u.id(`INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2)
		RETURNING gtfs_static_version_id`, u.server, at(publishedMinute))
}

func TestEpisodeViews_DerivedColumns(t *testing.T) {
	u := newUnmatchedViewFixture(t)
	stop := u.identity(stopKind, "1_123")

	openingResponse, opening := u.response(0)
	closingResponse, closing := u.response(30)
	episode := u.id(`INSERT INTO unmatched_stop_episode
		(reported_stop_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
		VALUES ($1, $2, $3, $4, $5, 'not_reported') RETURNING unmatched_stop_episode_id`,
		stop, u.agency, opening, closing, at(29))

	// The later-published version is inserted first, so it has the lower ID:
	// the view must pick by published_at, not by ID.
	later := u.staticVersion(20)
	earlier := u.staticVersion(10)
	metadata := func(version int64, found bool) int64 {
		id := u.id(`INSERT INTO stop_metadata_version (gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found)
			VALUES ($1, $2, $3, $4) RETURNING stop_metadata_version_id`, version, u.agency, stop, found)
		expectOK(t, u.exec(`INSERT INTO unmatched_stop_episode_metadata
			(unmatched_stop_episode_id, stop_metadata_version_id, reported_stop_id, agency_id) VALUES ($1, $2, $3, $4)`,
			episode, id, stop, u.agency))
		return id
	}
	laterMetadata := metadata(later, true)
	metadata(earlier, false)

	row := readView[dbgen.UnmatchedStopEpisodeView](t, u.fixture, "unmatched_stop_episode_view", "unmatched_stop_episode_id", episode)

	if row.ServerID != u.server || row.AgencyID != u.agency || row.StopID != "1_123" {
		t.Fatalf("server, agency or stop ID wrong: %+v", row)
	}
	if !row.OpenedAt.Equal(at(0)) {
		t.Fatalf("opened_at = %v, want %v", row.OpenedAt, at(0))
	}
	expectTime(t, "closed_at", row.ClosedAt, at(30))
	if row.OpeningResponseID != openingResponse || row.ClosingResponseID == nil || *row.ClosingResponseID != closingResponse {
		t.Fatalf("response IDs = %d, %v; want %d, %d", row.OpeningResponseID, show(row.ClosingResponseID), openingResponse, closingResponse)
	}
	if row.StaticFound == nil || !*row.StaticFound {
		t.Fatalf("static_found = %v, want the later version's true", show(row.StaticFound))
	}
	if row.StopMetadataVersionID == nil || *row.StopMetadataVersionID != laterMetadata ||
		row.GtfsStaticVersionID == nil || *row.GtfsStaticVersionID != later {
		t.Fatalf("latest context = metadata %v, version %v; want %d, %d",
			show(row.StopMetadataVersionID), show(row.GtfsStaticVersionID), laterMetadata, later)
	}

	t.Run("open episode without static context has nulls", func(t *testing.T) {
		other := u.identity(stopKind, "1_456")
		openEpisode := u.episode(stopKind, other, 40, open)
		row := readView[dbgen.UnmatchedStopEpisodeView](t, u.fixture, "unmatched_stop_episode_view", "unmatched_stop_episode_id", openEpisode)
		expectNullTime(t, "closed_at", row.ClosedAt)
		if row.ClosingResponseID != nil || row.StaticFound != nil || row.StopMetadataVersionID != nil || row.GtfsStaticVersionID != nil {
			t.Fatalf("open episode without metadata should have null closing and context columns: %+v", row)
		}
	})

	t.Run("trip view takes route, service and block from the latest context", func(t *testing.T) {
		trip := u.identity(tripKind, "1_trip")
		tripEpisode := u.episode(tripKind, trip, 0, open)
		for _, v := range []struct {
			version int64
			route   string
		}{{later, "R2"}, {earlier, "R1"}} {
			id := u.id(`INSERT INTO trip_metadata_version
				(gtfs_static_version_id, agency_id, reported_trip_id, static_trip_found, route_id, service_id, block_id)
				VALUES ($1, $2, $3, true, $4, 'weekday', 'B1') RETURNING trip_metadata_version_id`, v.version, u.agency, trip, v.route)
			expectOK(t, u.exec(`INSERT INTO unmatched_trip_episode_metadata
				(unmatched_trip_episode_id, trip_metadata_version_id, reported_trip_id, agency_id) VALUES ($1, $2, $3, $4)`,
				tripEpisode, id, trip, u.agency))
		}
		row := readView[dbgen.UnmatchedTripEpisodeView](t, u.fixture, "unmatched_trip_episode_view", "unmatched_trip_episode_id", tripEpisode)
		if row.TripID != "1_trip" || row.RouteID == nil || *row.RouteID != "R2" ||
			row.ServiceID == nil || *row.ServiceID != "weekday" || row.BlockID == nil || *row.BlockID != "B1" {
			t.Fatalf("trip context = %+v, want trip 1_trip, route R2, service weekday, block B1", row)
		}
	})
}

// recurrence is the expected previous_episode_count and previous_closed_at of
// one episode; previousClosedMinute is open when it should be null.
type recurrence struct {
	episode              int64
	count                int64
	previousClosedMinute int
}

func expectRecurrence(t *testing.T, name string, gotCount int64, gotClosed *time.Time, want recurrence) {
	t.Helper()
	if gotCount != want.count {
		t.Fatalf("%s: previous_episode_count = %d, want %d", name, gotCount, want.count)
	}
	if want.previousClosedMinute == open {
		expectNullTime(t, name+": previous_closed_at", gotClosed)
	} else {
		expectTime(t, name+": previous_closed_at", gotClosed, at(want.previousClosedMinute))
	}
}

func TestEpisodeViews_Recurrence(t *testing.T) {
	for _, k := range []unmatchedKind{stopKind, tripKind} {
		t.Run(k.view, func(t *testing.T) {
			u := newUnmatchedViewFixture(t)
			first := u.identity(k, "first")
			second := u.identity(k, "second")

			// The middle episode is inserted first, so episode IDs are not in
			// opening order: the view must count by opened_at.
			middle := u.episode(k, first, 20, 30)
			earliest := u.episode(k, first, 0, 10)
			latest := u.episode(k, first, 40, open)
			other := u.episode(k, second, 5, open)

			for _, want := range []recurrence{
				{earliest, 0, open},
				{middle, 1, 10},
				{latest, 2, 30},
				{other, 0, open},
			} {
				var count int64
				var closed *time.Time
				if err := u.pool.QueryRow(context.Background(),
					"SELECT previous_episode_count, previous_closed_at FROM "+k.view+" WHERE "+k.episodeIDColumn+" = $1",
					want.episode).Scan(&count, &closed); err != nil {
					t.Fatalf("read %s %d: %v", k.view, want.episode, err)
				}
				expectRecurrence(t, k.view, count, closed, want)
			}
		})
	}

	t.Run("vehicle_anomaly_episode_view", func(t *testing.T) {
		v := newVehicleFixture(t)
		otherType := v.id("SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = 'invalid_coordinate'")
		otherRule := v.id(`INSERT INTO vehicle_anomaly_rule_version
			(anomaly_type_id, rule_logic_version, version_name, effective_at)
			VALUES ($1, 1, 'logic 1', $2) RETURNING rule_version_id`, otherType, base)

		episode := func(anomalyType, rule int64, openedMinute, closedMinute int) int64 {
			opening := v.response(v.endpointA, at(openedMinute))
			var closing *int64
			var reason *string
			if closedMinute != open {
				c := v.response(v.endpointA, at(closedMinute))
				closing = &c
				r := "fresh_source_timestamp"
				reason = &r
			}
			return v.id(insertVehicleEpisode+" RETURNING vehicle_anomaly_episode_id",
				v.vehicle, anomalyType, rule, opening, closing, reason)
		}

		middle := episode(v.anomalyType, v.rule, 20, 30)
		earliest := episode(v.anomalyType, v.rule, 0, 10)
		latest := episode(v.anomalyType, v.rule, 40, open)
		// Same vehicle, other anomaly type: counted separately.
		other := episode(otherType, otherRule, 5, open)

		for _, want := range []recurrence{
			{earliest, 0, open},
			{middle, 1, 10},
			{latest, 2, 30},
			{other, 0, open},
		} {
			row := readView[dbgen.VehicleAnomalyEpisodeView](t, v.fixture, "vehicle_anomaly_episode_view", "vehicle_anomaly_episode_id", want.episode)
			expectRecurrence(t, "vehicle_anomaly_episode_view", row.PreviousEpisodeCount, row.PreviousClosedAt, want)
		}
	})
}

func TestEpisodeViews_VehicleOpeningEvidence(t *testing.T) {
	v := newVehicleFixture(t)
	server := v.id("SELECT server_id FROM vehicle_position_endpoint WHERE endpoint_id = $1", v.endpointA)
	agency := v.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'A') RETURNING agency_id", server)
	otherAgency := v.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'B') RETURNING agency_id", server)
	staticVersion := v.id(`INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2)
		RETURNING gtfs_static_version_id`, server, base)
	box := v.id(`INSERT INTO bounding_box_version
		(gtfs_static_version_id, server_id, minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		VALUES ($1, $2, 47, 48, -123, -122) RETURNING bounding_box_version_id`, staticVersion, server)

	link := func(episode, evidence int64, role string) {
		t.Helper()
		expectOK(t, v.exec(`INSERT INTO vehicle_anomaly_episode_evidence
			(vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role) VALUES ($1, $2, $3)`, episode, evidence, role))
	}

	opening := v.response(v.endpointA, at(0))
	change := v.response(v.endpointA, at(5))
	closing := v.response(v.endpointA, at(10))
	episode := v.id(insertVehicleEpisode+" RETURNING vehicle_anomaly_episode_id",
		v.vehicle, v.anomalyType, v.rule, opening, closing, "fresh_source_timestamp")

	openingEvidence := v.id(`INSERT INTO vehicle_position_evidence
		(vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid, position_validation_reason,
		 expected_schedule_state, expected_schedule_reason, route_id, gtfs_static_version_id, bounding_box_version_id)
		VALUES ($1, $2, $3, false, 'placeholder_zero_coordinate', 'unknown', 'no_trip_descriptor', 'R1', $4, $5)
		RETURNING vehicle_position_evidence_id`, opening, v.vehicle, agency, staticVersion, box)
	// Later evidence with different values must not leak into the view.
	changeEvidence := v.id(`INSERT INTO vehicle_position_evidence
		(vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid, route_id)
		VALUES ($1, $2, $3, true, 'R2') RETURNING vehicle_position_evidence_id`, change, v.vehicle, otherAgency)
	resolutionEvidence := v.id(`INSERT INTO vehicle_position_evidence
		(vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid, route_id)
		VALUES ($1, $2, $3, true, 'R3') RETURNING vehicle_position_evidence_id`, closing, v.vehicle, otherAgency)
	link(episode, changeEvidence, "meaningful_change")
	link(episode, openingEvidence, "opening")
	link(episode, resolutionEvidence, "resolution")

	row := readView[dbgen.VehicleAnomalyEpisodeView](t, v.fixture, "vehicle_anomaly_episode_view", "vehicle_anomaly_episode_id", episode)
	if row.ServerID != server || row.EndpointID != v.endpointA || row.VehicleID != "101" || row.AnomalyCode != "stale_source_timestamp" {
		t.Fatalf("identity columns wrong: %+v", row)
	}
	if !row.OpenedAt.Equal(at(0)) {
		t.Fatalf("opened_at = %v, want %v", row.OpenedAt, at(0))
	}
	expectTime(t, "closed_at", row.ClosedAt, at(10))
	if row.AgencyID == nil || *row.AgencyID != agency ||
		row.AttributionReason != nil ||
		row.PositionValidationReason == nil || *row.PositionValidationReason != "placeholder_zero_coordinate" ||
		row.ExpectedScheduleState == nil || *row.ExpectedScheduleState != "unknown" ||
		row.RouteID == nil || *row.RouteID != "R1" ||
		row.GtfsStaticVersionID == nil || *row.GtfsStaticVersionID != staticVersion ||
		row.BoundingBoxVersionID == nil || *row.BoundingBoxVersionID != box {
		t.Fatalf("evidence columns should come from the opening evidence: %+v", row)
	}

	t.Run("unattributed vehicle has a null agency", func(t *testing.T) {
		otherType := v.id("SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = 'invalid_coordinate'")
		otherRule := v.id(`INSERT INTO vehicle_anomaly_rule_version
			(anomaly_type_id, rule_logic_version, version_name, effective_at)
			VALUES ($1, 1, 'logic 1', $2) RETURNING rule_version_id`, otherType, base)
		response := v.response(v.endpointA, at(20))
		unattributed := v.id(insertVehicleEpisode+" RETURNING vehicle_anomaly_episode_id",
			v.vehicle, otherType, otherRule, response, nil, nil)
		evidence := v.id(`INSERT INTO vehicle_position_evidence
			(vehicle_position_response_id, vehicle_entity_id, attribution_reason, position_valid)
			VALUES ($1, $2, 'unknown_route_id', true) RETURNING vehicle_position_evidence_id`, response, v.vehicle)
		link(unattributed, evidence, "opening")

		row := readView[dbgen.VehicleAnomalyEpisodeView](t, v.fixture, "vehicle_anomaly_episode_view", "vehicle_anomaly_episode_id", unattributed)
		if row.AgencyID != nil {
			t.Fatalf("agency_id = %d, want null for an unattributed vehicle", *row.AgencyID)
		}
		if row.AttributionReason == nil || *row.AttributionReason != "unknown_route_id" {
			t.Fatalf("attribution_reason = %v, want unknown_route_id", show(row.AttributionReason))
		}
		expectNullTime(t, "closed_at", row.ClosedAt)
	})
}
