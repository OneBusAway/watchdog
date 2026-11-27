package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

func newEpisodeCountsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/episode-counts", s.getEpisodeCounts)
	router.HEAD("/v1/episode-counts", s.getEpisodeCounts)
	return router
}

type episodeCountsBody struct {
	From     string `json:"from"`
	To       string `json:"to"`
	ServerID *int64 `json:"server_id"`
	AgencyID *int64 `json:"agency_id"`
	Items    []struct {
		EpisodeType string           `json:"episode_type"`
		Opened      int64            `json:"opened"`
		Closed      map[string]int64 `json:"closed"`
		OpenAtTo    int64            `json:"open_at_to"`
	} `json:"items"`
	Links map[string]string `json:"links"`
}

func decodeEpisodeCounts(t *testing.T, rec *httptest.ResponseRecorder) episodeCountsBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body episodeCountsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

type trip struct {
	pool                               *pgxpool.Pool
	serverID, agencyID, reportedTripID int64
}

func addTrip(t *testing.T, pool *pgxpool.Pool, serverID, agencyID int64, tripID string) trip {
	t.Helper()
	id := dbtest.ID(t, pool,
		"INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, $2) RETURNING reported_trip_id", agencyID, tripID)
	return trip{pool: pool, serverID: serverID, agencyID: agencyID, reportedTripID: id}
}

func (tr trip) response(t *testing.T, at time.Time) (responseID, responseAgencyID int64) {
	t.Helper()
	responseID = dbtest.ID(t, tr.pool,
		"INSERT INTO onebusaway_metrics_response (server_id, observed_at) VALUES ($1, $2) RETURNING response_id",
		tr.serverID, at)
	responseAgencyID = dbtest.ID(t, tr.pool,
		"INSERT INTO onebusaway_metrics_response_agency (response_id, agency_id, response_complete) VALUES ($1, $2, true) RETURNING response_agency_id",
		responseID, tr.agencyID)
	return responseID, responseAgencyID
}

func (tr trip) episode(t *testing.T, opened, closed time.Time) int64 {
	t.Helper()
	_, first := tr.response(t, opened)
	var closing *int64
	var reason *string
	if !closed.IsZero() {
		_, id := tr.response(t, closed)
		closing = &id
		notReported := "not_reported"
		reason = &notReported
	}
	return dbtest.ID(t, tr.pool,
		`INSERT INTO unmatched_trip_episode
		 (reported_trip_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING unmatched_trip_episode_id`,
		tr.reportedTripID, tr.agencyID, first, closing, opened.Add(30*time.Second), reason)
}

type vehicleContext struct {
	pool          *pgxpool.Pool
	serverID      int64
	endpointID    int64
	vehicleEntity int64
}

func newVehicleContext(t *testing.T, pool *pgxpool.Pool, serverID int64) vehicleContext {
	t.Helper()
	endpointID := insertVehicleEndpoint(t, pool, serverID, "https://vp.example.com")
	vehID := insertVehicle(t, pool, endpointID, "veh-1")
	return vehicleContext{
		pool:          pool,
		serverID:      serverID,
		endpointID:    endpointID,
		vehicleEntity: vehID,
	}
}

func (vc vehicleContext) withVehicle(t *testing.T, vehicleRef string) vehicleContext {
	t.Helper()
	vehID := insertVehicle(t, vc.pool, vc.endpointID, vehicleRef)
	vc.vehicleEntity = vehID
	return vc
}

func (vc vehicleContext) episode(
	t *testing.T,
	anomalyCode string,
	opened, closed time.Time,
	closeReason *string,
	agencyID *int64,
) int64 {
	t.Helper()
	var typeID int16
	if err := vc.pool.QueryRow(context.Background(),
		"SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = $1", anomalyCode).Scan(&typeID); err != nil {
		t.Fatalf("query anomaly_type_id: %v", err)
	}

	var ruleVersionID int64
	err := vc.pool.QueryRow(context.Background(),
		"SELECT rule_version_id FROM vehicle_anomaly_rule_version WHERE anomaly_type_id = $1 LIMIT 1", typeID).Scan(&ruleVersionID)
	if err != nil {
		ruleVersionID = dbtest.ID(t, vc.pool,
			"INSERT INTO vehicle_anomaly_rule_version (anomaly_type_id, rule_logic_version, version_name, effective_at) VALUES ($1, 1, 'v1', now()) RETURNING rule_version_id",
			typeID)
	}

	openingResponseID := dbtest.ID(t, vc.pool,
		"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
		vc.endpointID, opened)

	var closingResponseID *int64
	if !closed.IsZero() {
		id := dbtest.ID(t, vc.pool,
			"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
			vc.endpointID, closed)
		closingResponseID = &id
		if closeReason == nil {
			defaultReason := "fresh_source_timestamp"
			closeReason = &defaultReason
		}
	}

	epID := dbtest.ID(t, vc.pool,
		`INSERT INTO vehicle_anomaly_episode
		 (vehicle_entity_id, anomaly_type_id, rule_version_id, opening_response_id, closing_response_id, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING vehicle_anomaly_episode_id`,
		vc.vehicleEntity, typeID, ruleVersionID, openingResponseID, closingResponseID, closeReason)

	if agencyID != nil {
		evidenceID := dbtest.ID(t, vc.pool,
			`INSERT INTO vehicle_position_evidence
			 (vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid)
			 VALUES ($1, $2, $3, true) RETURNING vehicle_position_evidence_id`,
			openingResponseID, vc.vehicleEntity, *agencyID)
		if err := dbtest.Exec(vc.pool,
			`INSERT INTO vehicle_anomaly_episode_evidence
			 (vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role)
			 VALUES ($1, $2, 'opening')`,
			epID, evidenceID); err != nil {
			t.Fatalf("insert episode evidence: %v", err)
		}
	}
	return epID
}

func TestGetEpisodeCounts_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/episode-counts"); h == nil {
			t.Errorf("%s /v1/episode-counts is not registered", method)
		}
	}
}

func TestGetEpisodeCounts_AllTypesPresent(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newEpisodeCountsRouter(t, pool)

	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z", nil)
	body := decodeEpisodeCounts(t, rec)

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("ETag is empty")
	}

	if len(body.Items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(body.Items))
	}

	expectedTypes := []string{
		"unmatched_stop",
		"unmatched_trip",
		"stale_source_timestamp",
		"invalid_coordinate",
		"stopped_out_of_bounds",
	}

	for i, wantType := range expectedTypes {
		item := body.Items[i]
		if item.EpisodeType != wantType {
			t.Errorf("item[%d].EpisodeType = %q, want %q", i, item.EpisodeType, wantType)
		}
		if item.Opened != 0 {
			t.Errorf("item[%d].Opened = %d, want 0", i, item.Opened)
		}
		if item.OpenAtTo != 0 {
			t.Errorf("item[%d].OpenAtTo = %d, want 0", i, item.OpenAtTo)
		}

		if i == 0 {
			if len(item.Closed) != 3 || item.Closed["not_reported"] != 0 ||
				item.Closed["agency_unlisted"] != 0 || item.Closed["agency_removed"] != 0 {
				t.Errorf("item[%d].Closed = %+v, want not_reported, agency_unlisted and agency_removed at 0", i, item.Closed)
			}
		} else if i == 1 {
			if len(item.Closed) != 1 || item.Closed["not_reported"] != 0 {
				t.Errorf("item[%d].Closed = %+v, want not_reported: 0", i, item.Closed)
			}
		} else {
			if len(item.Closed) != 5 {
				t.Errorf("item[%d].Closed has %d entries, want 5", i, len(item.Closed))
			}
			expectedCloseReasons := []string{
				"fresh_source_timestamp",
				"not_present_in_full_dataset",
				"coordinates_valid_again",
				"inside_bounding_box",
				"no_longer_stopped_at_stop",
			}
			for _, r := range expectedCloseReasons {
				val, ok := item.Closed[r]
				if !ok {
					t.Errorf("item[%d].Closed missing expected key %q", i, r)
				} else if val != 0 {
					t.Errorf("item[%d].Closed[%q] = %d, want 0", i, r, val)
				}
			}
		}
	}
}

func TestGetEpisodeCounts_Definitions(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)

	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "agency-1")
	st := addStop(t, pool, serverID, agencyID, "stop-1")
	tr := addTrip(t, pool, serverID, agencyID, "trip-1")
	vc := newVehicleContext(t, pool, serverID)

	t0 := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) // Window from
	t1Mid := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) // Window to
	t3 := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	// --- 1. Unmatched Stop Episodes ---
	// Ep 1: opened before window (t0), closed inside window (t1Mid) -> closed +1
	st.episode(t, t0, t1Mid)
	// Ep 2: opened inside window (t1Mid), still open -> opened +1, open_at_to +1
	st.episode(t, t1Mid, time.Time{})
	// Ep 3: opened inside window (t1Mid), closed inside window (t1Mid + 1h) -> opened +1, closed +1
	st.episode(t, t1Mid, t1Mid.Add(time.Hour))
	// Ep 4: opened inside window (t1Mid), closed after window (t3) -> opened +1, open_at_to +1
	st.episode(t, t1Mid, t3)
	// Ep 5: opened after window (t3), closed after window (t3 + 1h) -> none
	st.episode(t, t3, t3.Add(time.Hour))

	// Boundaries for window [t1, t2):
	// Boundary 1: opened_at == t2 (half-open, not counted in opened)
	stBound := addStop(t, pool, serverID, agencyID, "stop-bound")
	stBound.episode(t, t2, time.Time{})
	// Boundary 2: closed_at == t1 (closed_at >= t1, so counted in closed)
	st.episode(t, t0, t1)
	// Boundary 3: closed_at == t2 (closed_at < t2 is false, so not counted in closed)
	st.episode(t, t0, t2)

	// --- 2. Unmatched Trip Episodes ---
	// Ep: opened inside window (t1Mid), closed inside window (t1Mid + 2h)
	tr.episode(t, t1Mid, t1Mid.Add(2*time.Hour))

	// --- 3. Vehicle Anomaly Episodes ---
	freshReason := "fresh_source_timestamp"
	coordReason := "coordinates_valid_again"

	// stale_source_timestamp:
	// Ep A: opened inside window (t1Mid), closed inside window with fresh_source_timestamp
	vc.episode(t, "stale_source_timestamp", t1Mid, t1Mid.Add(time.Hour), &freshReason, &agencyID)
	// Ep B: opened inside window (t1Mid), still open
	vc.episode(t, "stale_source_timestamp", t1Mid, time.Time{}, nil, &agencyID)

	// invalid_coordinate:
	// Ep C: opened before window (t0), closed inside window (t1Mid) with coordinates_valid_again
	vc.episode(t, "invalid_coordinate", t0, t1Mid, &coordReason, &agencyID)

	// stopped_out_of_bounds:
	// Ep D: opened inside window (t1Mid), closed inside window (t1Mid + 1h) with inside_bounding_box
	boundsReason := "inside_bounding_box"
	vcBounds := vc.withVehicle(t, "veh-bounds")
	vcBounds.episode(t, "stopped_out_of_bounds", t1Mid, t1Mid.Add(time.Hour), &boundsReason, &agencyID)

	router := newEpisodeCountsRouter(t, pool)
	path := "/v1/episode-counts?from=" + t1.Format(time.RFC3339) + "&to=" + t2.Format(time.RFC3339)
	rec := doGet(t, router, path, nil)
	body := decodeEpisodeCounts(t, rec)

	// Verify unmatched_stop
	stopItem := body.Items[0]
	if stopItem.EpisodeType != "unmatched_stop" {
		t.Fatalf("items[0] = %s, want unmatched_stop", stopItem.EpisodeType)
	}
	// Opened: Ep 2, Ep 3, Ep 4 = 3 (Boundary 1 has opened_at == t2, excluded by opened_at < t2)
	if stopItem.Opened != 3 {
		t.Errorf("stopItem.Opened = %d, want 3", stopItem.Opened)
	}
	// Closed: Ep 1, Ep 3, Boundary 2 (closed_at == t1) = 3 (Boundary 3 has closed_at == t2, excluded by closed_at < t2)
	if stopItem.Closed["not_reported"] != 3 {
		t.Errorf("stopItem.Closed[not_reported] = %d, want 3", stopItem.Closed["not_reported"])
	}
	// Open at to: Ep 2 (open), Ep 4 (closed at t3 > t2), Boundary 3 (closed_at == t2 >= t2) = 3
	if stopItem.OpenAtTo != 3 {
		t.Errorf("stopItem.OpenAtTo = %d, want 3", stopItem.OpenAtTo)
	}

	// Verify unmatched_trip
	tripItem := body.Items[1]
	if tripItem.EpisodeType != "unmatched_trip" {
		t.Fatalf("items[1] = %s, want unmatched_trip", tripItem.EpisodeType)
	}
	if tripItem.Opened != 1 {
		t.Errorf("tripItem.Opened = %d, want 1", tripItem.Opened)
	}
	if tripItem.Closed["not_reported"] != 1 {
		t.Errorf("tripItem.Closed[not_reported] = %d, want 1", tripItem.Closed["not_reported"])
	}
	if tripItem.OpenAtTo != 0 {
		t.Errorf("tripItem.OpenAtTo = %d, want 0", tripItem.OpenAtTo)
	}

	// Verify stale_source_timestamp
	staleItem := body.Items[2]
	if staleItem.EpisodeType != "stale_source_timestamp" {
		t.Fatalf("items[2] = %s, want stale_source_timestamp", staleItem.EpisodeType)
	}
	if staleItem.Opened != 2 {
		t.Errorf("staleItem.Opened = %d, want 2", staleItem.Opened)
	}
	if staleItem.Closed["fresh_source_timestamp"] != 1 {
		t.Errorf("staleItem.Closed[fresh_source_timestamp] = %d, want 1", staleItem.Closed["fresh_source_timestamp"])
	}
	if staleItem.OpenAtTo != 1 {
		t.Errorf("staleItem.OpenAtTo = %d, want 1", staleItem.OpenAtTo)
	}

	// Verify invalid_coordinate
	coordItem := body.Items[3]
	if coordItem.EpisodeType != "invalid_coordinate" {
		t.Fatalf("items[3] = %s, want invalid_coordinate", coordItem.EpisodeType)
	}
	if coordItem.Opened != 0 {
		t.Errorf("coordItem.Opened = %d, want 0", coordItem.Opened)
	}
	if coordItem.Closed["coordinates_valid_again"] != 1 {
		t.Errorf("coordItem.Closed[coordinates_valid_again] = %d, want 1", coordItem.Closed["coordinates_valid_again"])
	}
	if coordItem.OpenAtTo != 0 {
		t.Errorf("coordItem.OpenAtTo = %d, want 0", coordItem.OpenAtTo)
	}

	// Verify stopped_out_of_bounds
	boundsItem := body.Items[4]
	if boundsItem.EpisodeType != "stopped_out_of_bounds" {
		t.Fatalf("items[4] = %s, want stopped_out_of_bounds", boundsItem.EpisodeType)
	}
	if boundsItem.Opened != 1 {
		t.Errorf("boundsItem.Opened = %d, want 1", boundsItem.Opened)
	}
	if boundsItem.Closed["inside_bounding_box"] != 1 {
		t.Errorf("boundsItem.Closed[inside_bounding_box] = %d, want 1", boundsItem.Closed["inside_bounding_box"])
	}
	if boundsItem.OpenAtTo != 0 {
		t.Errorf("boundsItem.OpenAtTo = %d, want 0", boundsItem.OpenAtTo)
	}
}

func TestGetEpisodeCounts_AgencyFilter(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)

	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyA := insertEpisodeAgency(t, pool, serverID, "agency-a")
	agencyB := insertEpisodeAgency(t, pool, serverID, "agency-b")

	stopA := addStop(t, pool, serverID, agencyA, "stop-a")
	stopB := addStop(t, pool, serverID, agencyB, "stop-b")

	vcA := newVehicleContext(t, pool, serverID)
	vcB := vcA.withVehicle(t, "veh-b")

	tOpen := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	stopA.episode(t, tOpen, time.Time{})
	stopB.episode(t, tOpen, time.Time{})

	// Vehicle episode attributed to Agency A via opening evidence
	vcA.episode(t, "stale_source_timestamp", tOpen, time.Time{}, nil, &agencyA)
	// Vehicle episode attributed to Agency B via opening evidence
	vcB.episode(t, "stale_source_timestamp", tOpen, time.Time{}, nil, &agencyB)

	router := newEpisodeCountsRouter(t, pool)

	// Filter by agencyA
	pathA := "/v1/episode-counts?agency_id=" + agencyIDString(agencyA) + "&from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"
	recA := doGet(t, router, pathA, nil)
	bodyA := decodeEpisodeCounts(t, recA)

	if bodyA.AgencyID == nil || *bodyA.AgencyID != agencyA {
		t.Errorf("bodyA.AgencyID = %v, want %d", bodyA.AgencyID, agencyA)
	}
	if bodyA.Items[0].Opened != 1 {
		t.Errorf("agencyA stop Opened = %d, want 1", bodyA.Items[0].Opened)
	}
	if bodyA.Items[2].Opened != 1 {
		t.Errorf("agencyA vehicle Opened = %d, want 1", bodyA.Items[2].Opened)
	}

	// Filter by agencyB
	pathB := "/v1/episode-counts?agency_id=" + agencyIDString(agencyB) + "&from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"
	recB := doGet(t, router, pathB, nil)
	bodyB := decodeEpisodeCounts(t, recB)

	if bodyB.AgencyID == nil || *bodyB.AgencyID != agencyB {
		t.Errorf("bodyB.AgencyID = %v, want %d", bodyB.AgencyID, agencyB)
	}
	if bodyB.Items[0].Opened != 1 {
		t.Errorf("agencyB stop Opened = %d, want 1", bodyB.Items[0].Opened)
	}
	if bodyB.Items[2].Opened != 1 {
		t.Errorf("agencyB vehicle Opened = %d, want 1", bodyB.Items[2].Opened)
	}
}

func TestGetEpisodeCounts_ServerFilter(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)

	server1 := insertEpisodeServer(t, pool, "https://s1.example.com")
	server2 := insertEpisodeServer(t, pool, "https://s2.example.com")

	agency1 := insertEpisodeAgency(t, pool, server1, "a1")
	agency2 := insertEpisodeAgency(t, pool, server2, "a2")

	stop1 := addStop(t, pool, server1, agency1, "stop-1")
	stop2 := addStop(t, pool, server2, agency2, "stop-2")

	tOpen := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	stop1.episode(t, tOpen, time.Time{})
	stop2.episode(t, tOpen, time.Time{})

	router := newEpisodeCountsRouter(t, pool)

	path := "/v1/episode-counts?server_id=" + agencyIDString(server1) + "&from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"
	rec := doGet(t, router, path, nil)
	body := decodeEpisodeCounts(t, rec)

	if body.ServerID == nil || *body.ServerID != server1 {
		t.Errorf("body.ServerID = %v, want %d", body.ServerID, server1)
	}
	if body.Items[0].Opened != 1 {
		t.Errorf("filtered Opened = %d, want 1", body.Items[0].Opened)
	}
}

func TestGetEpisodeCounts_Window(t *testing.T) {
	router := newEpisodeCountsRouter(t, nil)

	tests := []struct {
		name string
		path string
	}{
		{"missing_from", "/v1/episode-counts?to=2026-10-24T00:00:00Z"},
		{"missing_to", "/v1/episode-counts?from=2026-10-17T00:00:00Z"},
		{"missing_both", "/v1/episode-counts"},
		{"from_after_to", "/v1/episode-counts?from=2026-10-24T00:00:00Z&to=2026-10-17T00:00:00Z"},
		{"from_equals_to", "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-17T00:00:00Z"},
		{"over_31_days", "/v1/episode-counts?from=2026-10-01T00:00:00Z&to=2026-11-05T00:00:00Z"},
		{"invalid_from_time", "/v1/episode-counts?from=invalid&to=2026-10-24T00:00:00Z"},
		{"no_timezone_offset", "/v1/episode-counts?from=2026-10-17T00:00:00&to=2026-10-24T00:00:00Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, router, tc.path, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
		})
	}
}

func TestGetEpisodeCounts_RejectsUnknownParameter(t *testing.T) {
	router := newEpisodeCountsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&foo=1", nil), "foo")
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&include=agency", nil), "include")
}

func TestGetEpisodeCounts_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newEpisodeCountsRouter(t, pool)

	path := "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"
	get := doGet(t, router, path, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("response has no ETag")
	}

	revalidate := doGet(t, router, path, map[string]string{
		"If-None-Match": etag,
	})
	if revalidate.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", revalidate.Code)
	}
	if revalidate.Body.Len() > 0 {
		t.Errorf("304 has body: %s", revalidate.Body)
	}
}

func TestGetEpisodeCounts_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newEpisodeCountsRouter(t, pool)

	path := "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"
	get := doGet(t, router, path, nil)
	req := httptest.NewRequest(http.MethodHead, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
	if got := head.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
}

func agencyIDString(id int64) string {
	return strconvFormat(id)
}

func strconvFormat(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

func TestGetEpisodeCounts_GroupRequiresType(t *testing.T) {
	router := newEpisodeCountsRouter(t, nil)
	fromTo := "from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"

	// static_found without episode_type -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=static_found", nil), "group_by")

	// static_found with invalid_coordinate -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=static_found&episode_type=invalid_coordinate", nil), "group_by")

	// cluster with unmatched_trip -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=cluster&episode_type=unmatched_trip", nil), "group_by")

	// endpoint without episode_type -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=endpoint", nil), "group_by")

	// endpoint with unmatched_stop -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=endpoint&episode_type=unmatched_stop", nil), "group_by")

	// unknown group_by -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&group_by=invalid_dimension", nil), "group_by")

	// unknown episode_type -> 400
	wantInvalidParam(t, doGet(t, router, "/v1/episode-counts?"+fromTo+"&episode_type=invalid_type", nil), "episode_type")
}

func TestGetEpisodeCounts_OpenedHourWindow(t *testing.T) {
	router := newEpisodeCountsRouter(t, nil)

	// 8 days window with opened_hour -> 400
	rec8 := doGet(t, router, "/v1/episode-counts?from=2026-10-16T00:00:00Z&to=2026-10-24T00:00:00Z&group_by=opened_hour", nil)
	wantInvalidParam(t, rec8, "to")

	// 7 days window with opened_hour -> 200 (even without pool if no DB needed or with pool)
}

func TestGetEpisodeCounts_EpisodeTypeOnly(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	st := addStop(t, pool, serverID, agencyID, "1_100")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	st.episode(t, opened, time.Time{})

	router := newEpisodeCountsRouter(t, pool)

	// episode_type=unmatched_stop alone
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["episode_type"] != "unmatched_stop" {
		t.Errorf("episode_type = %v, want unmatched_stop", body["episode_type"])
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	it := items[0].(map[string]any)
	if it["episode_type"] != "unmatched_stop" || int64(it["opened"].(float64)) != 1 {
		t.Errorf("unexpected item: %+v", it)
	}
}

func TestGetEpisodeCounts_GroupLinks(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	st1 := addStop(t, pool, serverID, agencyID, "1_101")
	st2 := addStop(t, pool, serverID, agencyID, "1_102")

	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	ep1 := st1.episode(t, opened, time.Time{})
	ep2 := st2.episode(t, opened.Add(time.Hour), time.Time{})

	st1.linkStatic(t, ep1, opened.Add(-time.Hour), true)
	st2.linkStatic(t, ep2, opened.Add(-time.Hour), false)

	s, router := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }

	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=static_found", map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	for _, raw := range items {
		it := raw.(map[string]any)
		val := it["value"].(bool)
		links := it["links"].(map[string]any)
		epLink := links["episodes"].(string)

		epRec := doGet(t, router, epLink, map[string]string{"Authorization": "Bearer " + testToken})
		if epRec.Code != http.StatusOK {
			t.Fatalf("GET episodes link %s status = %d, want 200", epLink, epRec.Code)
		}
		var epBody map[string]any
		json.Unmarshal(epRec.Body.Bytes(), &epBody)
		epItems := epBody["items"].([]any)
		if len(epItems) != 1 {
			t.Fatalf("episodes count = %d, want 1", len(epItems))
		}
		gotID := int64(epItems[0].(map[string]any)["unmatched_stop_episode_id"].(float64))
		if val && gotID != ep1 {
			t.Errorf("static_found=true returned %d, want %d", gotID, ep1)
		}
		if !val && gotID != ep2 {
			t.Errorf("static_found=false returned %d, want %d", gotID, ep2)
		}
	}
}

func TestGetEpisodeCounts_NullGroups(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	vc := newVehicleContext(t, pool, serverID)

	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	// 1. Unattributed vehicle (agency_id = nil)
	vc.episode(t, episodeTypeInvalidCoordinate, opened, time.Time{}, nil, nil)

	router := newEpisodeCountsRouter(t, pool)

	// group_by=agency on vehicle -> null group with agency_id=none
	recAgency := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=invalid_coordinate&group_by=agency", nil)
	if recAgency.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recAgency.Code)
	}
	var bodyAgency map[string]any
	json.Unmarshal(recAgency.Body.Bytes(), &bodyAgency)
	itemsAgency := bodyAgency["items"].([]any)
	if len(itemsAgency) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(itemsAgency))
	}
	itAgency := itemsAgency[0].(map[string]any)
	if itAgency["value"] != nil {
		t.Errorf("agency value = %v, want nil", itAgency["value"])
	}
	linksAgency := itAgency["links"].(map[string]any)
	epLinkAgency := linksAgency["episodes"].(string)
	if !strings.Contains(epLinkAgency, "agency_id=none") {
		t.Errorf("expected agency_id=none in %s", epLinkAgency)
	}

	// 2. Open episode -> closing_response=nil has status=open link
	recClose := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=invalid_coordinate&group_by=closing_response", nil)
	var bodyClose map[string]any
	json.Unmarshal(recClose.Body.Bytes(), &bodyClose)
	itClose := bodyClose["items"].([]any)[0].(map[string]any)
	if itClose["value"] != nil {
		t.Errorf("closing_response value = %v, want nil", itClose["value"])
	}
	epLinkClose := itClose["links"].(map[string]any)["episodes"].(string)
	if !strings.Contains(epLinkClose, "status=open") {
		t.Errorf("expected status=open in %s", epLinkClose)
	}

	// 3. Null route_id -> no episodes link
	tr := addTrip(t, pool, serverID, agencyID, "trip-noroute")
	tr.episode(t, opened, time.Time{})

	recRoute := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_trip&group_by=route_id", nil)
	var bodyRoute map[string]any
	json.Unmarshal(recRoute.Body.Bytes(), &bodyRoute)
	itRoute := bodyRoute["items"].([]any)[0].(map[string]any)
	if itRoute["value"] != nil {
		t.Errorf("route_id value = %v, want nil", itRoute["value"])
	}
	if itRoute["links"] != nil {
		l := itRoute["links"].(map[string]any)
		if _, ok := l["episodes"]; ok {
			t.Errorf("expected no episodes link on null route_id, got %v", l)
		}
	}
}

func TestGetEpisodeCounts_Truncated(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	// Create 201 stops with distinct entities
	for i := 0; i < 205; i++ {
		st := addStop(t, pool, serverID, agencyID, fmt.Sprintf("stop_%d", i))
		st.episode(t, opened, time.Time{})
	}

	router := newEpisodeCountsRouter(t, pool)
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=entity", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["groups_truncated"] != true {
		t.Errorf("groups_truncated = %v, want true", body["groups_truncated"])
	}
	items := body["items"].([]any)
	if len(items) != 200 {
		t.Errorf("len(items) = %d, want 200", len(items))
	}
}

func TestGetEpisodeCounts_ClusterScheme(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	st := addStop(t, pool, serverID, agencyID, "stop-clust")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	ep := st.episode(t, opened, time.Time{})

	seedScheme(t, pool, "s1", episodesNow.Add(-10*time.Hour))
	seedCell(t, pool, "s1", "cell1")
	metaID := linkVersion(t, st, ep, opened.Add(-time.Hour))
	station := "st-A"
	assign(t, pool, metaID, "s1", "cell1", &station)

	router := newEpisodeCountsRouter(t, pool)

	// 1. Defaults to newest scheme
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=cluster", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	it := items[0].(map[string]any)
	val := it["value"].(map[string]any)
	if val["cell_id"] != "cell1" || val["root_station_id"] != "st-A" {
		t.Errorf("cluster value = %+v, want cell1 and st-A", val)
	}

	// 2. Unknown scheme -> 400
	recBad := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=cluster&scheme_version=nonexistent", nil)
	wantInvalidParam(t, recBad, "scheme_version")
}

func TestGetEpisodeCounts_Dimensions(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agency1 := insertEpisodeAgency(t, pool, serverID, "1")
	agency2 := insertEpisodeAgency(t, pool, serverID, "2")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	// Stops for recurrence
	st1 := addStop(t, pool, serverID, agency1, "stop-rec-1")
	st1.episode(t, opened.Add(-48*time.Hour), opened.Add(-24*time.Hour))
	st1.episode(t, opened, time.Time{})

	st2 := addStop(t, pool, serverID, agency2, "stop-rec-2")
	st2.episode(t, opened, time.Time{})

	router := newEpisodeCountsRouter(t, pool)

	// 1. group_by=agency across all types
	recAgency := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&group_by=agency", nil)
	if recAgency.Code != http.StatusOK {
		t.Fatalf("agency status = %d, want 200", recAgency.Code)
	}

	// 2. group_by=recurrence
	recRec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=recurrence", nil)
	if recRec.Code != http.StatusOK {
		t.Fatalf("recurrence status = %d, want 200", recRec.Code)
	}
	var bodyRec map[string]any
	json.Unmarshal(recRec.Body.Bytes(), &bodyRec)
	itemsRec := bodyRec["items"].([]any)
	if len(itemsRec) != 2 {
		t.Fatalf("recurrence items = %d, want 2", len(itemsRec))
	}

	// 3. group_by=opened_hour & opened_day
	recHour := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=opened_hour", nil)
	if recHour.Code != http.StatusOK {
		t.Fatalf("opened_hour status = %d, want 200", recHour.Code)
	}
	recDay := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=opened_day", nil)
	if recDay.Code != http.StatusOK {
		t.Fatalf("opened_day status = %d, want 200", recDay.Code)
	}

	// 4. Vehicle dimensions
	vc := newVehicleContext(t, pool, serverID)
	// Seed vehicle anomaly with evidence
	vStatic := dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		serverID, episodesNow.Add(-10*time.Hour))
	bboxID := dbtest.ID(t, pool,
		`INSERT INTO bounding_box_version (gtfs_static_version_id, server_id, minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		 VALUES ($1, $2, 47.0, 48.0, -123.0, -122.0) RETURNING bounding_box_version_id`,
		vStatic, serverID)
	respID := dbtest.ID(t, pool,
		"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
		vc.endpointID, opened)
	evID := dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_evidence
		 (vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid,
		  route_id, bounding_box_version_id, position_validation_reason, attribution_reason, expected_schedule_state, expected_schedule_reason)
		 VALUES ($1, $2, NULL, false, 'route-44', $3, 'latitude_out_of_range', 'unknown_route_id', 'expected_active', 'within_scheduled_window')
		 RETURNING vehicle_position_evidence_id`,
		respID, vc.vehicleEntity, bboxID)

	var typeID int16
	pool.QueryRow(context.Background(), "SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = 'stopped_out_of_bounds'").Scan(&typeID)
	ruleID := dbtest.ID(t, pool,
		"INSERT INTO vehicle_anomaly_rule_version (anomaly_type_id, rule_logic_version, version_name, effective_at) VALUES ($1, 1, 'v1', now()) RETURNING rule_version_id",
		typeID)
	vehEp := dbtest.ID(t, pool,
		`INSERT INTO vehicle_anomaly_episode
		 (vehicle_entity_id, anomaly_type_id, rule_version_id, opening_response_id, closing_response_id, close_reason)
		 VALUES ($1, $2, $3, $4, NULL, NULL) RETURNING vehicle_anomaly_episode_id`,
		vc.vehicleEntity, typeID, ruleID, respID)
	dbtest.Exec(pool,
		`INSERT INTO vehicle_anomaly_episode_evidence (vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role)
		 VALUES ($1, $2, 'opening')`, vehEp, evID)

	// test endpoint
	recEnd := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=endpoint", nil)
	if recEnd.Code != http.StatusOK {
		t.Fatalf("endpoint status = %d, want 200", recEnd.Code)
	}
	// test route_id
	recRoute := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=route_id", nil)
	if recRoute.Code != http.StatusOK {
		t.Fatalf("route_id status = %d, want 200", recRoute.Code)
	}
	// test rule_version
	recRule := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=rule_version", nil)
	if recRule.Code != http.StatusOK {
		t.Fatalf("rule_version status = %d, want 200", recRule.Code)
	}
	// test bounding_box_version
	recBBox := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=bounding_box_version", nil)
	if recBBox.Code != http.StatusOK {
		t.Fatalf("bounding_box_version status = %d, want 200", recBBox.Code)
	}
	// test position_validation_reason
	recPos := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=invalid_coordinate&group_by=position_validation_reason", nil)
	if recPos.Code != http.StatusOK {
		t.Fatalf("position_validation_reason status = %d, want 200", recPos.Code)
	}
	// test attribution_reason
	recAttr := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=attribution_reason", nil)
	if recAttr.Code != http.StatusOK {
		t.Fatalf("attribution_reason status = %d, want 200", recAttr.Code)
	}
	// test expected_schedule_state
	recSched := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=stopped_out_of_bounds&group_by=expected_schedule_state", nil)
	if recSched.Code != http.StatusOK {
		t.Fatalf("expected_schedule_state status = %d, want 200", recSched.Code)
	}
}

func TestGetEpisodeCounts_Include(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	st := addStop(t, pool, serverID, agencyID, "stop-inc")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	st.episode(t, opened, time.Time{})

	s, router := newTestService(t)
	s.Pool = pool

	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=agency&include=agency", map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	inc, ok := body["included"].(map[string]any)
	if !ok || len(inc) != 1 {
		t.Fatalf("included = %+v, want 1 agency", inc)
	}
	agencyURL := fmt.Sprintf("/v1/agencies/%d", agencyID)
	if _, ok := inc[agencyURL]; !ok {
		t.Errorf("missing included URL %s in %+v", agencyURL, inc)
	}
}

func TestGetEpisodeCounts_StaticFoundNullGroup(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	st := addStop(t, pool, serverID, agencyID, "stop-no-static")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	// Episode with no static metadata linked -> static_found is NULL
	st.episode(t, opened, time.Time{})

	router := newEpisodeCountsRouter(t, pool)
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=static_found", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	it := items[0].(map[string]any)
	if it["value"] != nil {
		t.Errorf("value = %v, want nil", it["value"])
	}
	if it["links"] != nil {
		l := it["links"].(map[string]any)
		if _, ok := l["episodes"]; ok {
			t.Errorf("expected no links.episodes on null static_found group, got %v", l)
		}
	}
}

func TestGetEpisodeCounts_EntityMixedTypes(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	// Stop episode
	st := addStop(t, pool, serverID, agencyID, "stop-1")
	st.episode(t, opened, time.Time{})

	// Trip episode
	tr := addTrip(t, pool, serverID, agencyID, "trip-1")
	tr.episode(t, opened, time.Time{})

	// Vehicle episode
	vc := newVehicleContext(t, pool, serverID)
	vc.episode(t, episodeTypeInvalidCoordinate, opened, time.Time{}, nil, &agencyID)

	router := newEpisodeCountsRouter(t, pool)
	// group_by=entity without episode_type -> all 3 types present with opened=1
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&group_by=entity", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
}

func TestGetEpisodeCounts_OpenedHourNoPreWindowBuckets(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")

	// Episode 1: opened 20 days ago, still open
	st1 := addStop(t, pool, serverID, agencyID, "stop-old")
	st1.episode(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), time.Time{})

	// Episode 2: opened in window (2026-10-20)
	st2 := addStop(t, pool, serverID, agencyID, "stop-in-window")
	st2.episode(t, time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), time.Time{})

	router := newEpisodeCountsRouter(t, pool)
	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=opened_hour", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want exactly 1 in-window bucket; got %+v", len(items), items)
	}
	it := items[0].(map[string]any)
	if it["value"] != "2026-10-20T10:00:00Z" {
		t.Errorf("bucket = %v, want 2026-10-20T10:00:00Z", it["value"])
	}
}

func TestGetEpisodeCounts_IncludeTruncatedLinks(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	opened := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	// 205 stops
	for i := 0; i < 205; i++ {
		st := addStop(t, pool, serverID, agencyID, fmt.Sprintf("trunc_stop_%d", i))
		st.episode(t, opened, time.Time{})
	}

	s, router := newTestService(t)
	s.Pool = pool

	rec := doGet(t, router, "/v1/episode-counts?from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=entity&include=reported_stop",
		map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 200 {
		t.Fatalf("len(items) = %d, want 200", len(items))
	}
	inc := body["included"].(map[string]any)
	if len(inc) != 200 {
		t.Fatalf("len(included) = %d, want 200", len(inc))
	}

	// Verify that every returned item's reported_stop link exists in included
	for _, raw := range items {
		it := raw.(map[string]any)
		l := it["links"].(map[string]any)
		stopURL := l["reported_stop"].(string)
		if _, ok := inc[stopURL]; !ok {
			t.Fatalf("reported_stop link %s not found in included", stopURL)
		}
	}
}

// TestGetEpisodeCounts_VehicleResponseIncludeAccepted checks that opening_response
// and closing_response can be included for a vehicle type, now that the vehicle
// response loader (E28) exists.
func TestGetEpisodeCounts_VehicleResponseIncludeAccepted(t *testing.T) {
	router := newEpisodeCountsRouter(t, dbtest.NewMigratedPool(t))
	fromTo := "from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z"

	for _, name := range []string{"opening_response", "closing_response"} {
		rec := doGet(t, router, "/v1/episode-counts?"+fromTo+"&episode_type=invalid_coordinate&group_by="+name+"&include="+name, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("include=%s: status = %d, want 200; body %s", name, rec.Code, rec.Body)
		}
	}
}
