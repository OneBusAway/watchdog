package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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

		if i < 2 {
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
