package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

const vehiclePositionEvidencePath = "/v1/vehicle-position-evidence"

// newVehiclePositionEvidenceRouter mounts E27 for GET and HEAD on a service using pool.
// newTestService registers every loader, so include resolves real loaders.
func newVehiclePositionEvidenceRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET(vehiclePositionEvidencePath+"/:evidence_id", s.getVehiclePositionEvidence)
	router.HEAD(vehiclePositionEvidencePath+"/:evidence_id", s.getVehiclePositionEvidence)
	return router
}

// evidenceFixture is the set of parent rows one evidence record needs.
type evidenceFixture struct {
	serverID, endpointID, vehicleEntityID, agencyID, staticVersionID, boxID, responseID int64
	observed                                                                            time.Time
}

// newEvidenceFixture inserts a server, endpoint, vehicle, agency, static version,
// bounding box and position response.
func newEvidenceFixture(t *testing.T, pool *pgxpool.Pool) evidenceFixture {
	t.Helper()
	var f evidenceFixture
	f.observed = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	f.serverID = insertVehicleServer(t, pool, "https://oba.example.org")
	f.endpointID = insertVehicleEndpoint(t, pool, f.serverID, "https://oba.example.org/vehicles.pb")
	f.vehicleEntityID = insertVehicle(t, pool, f.endpointID, "bus-1")
	f.agencyID = insertEpisodeAgency(t, pool, f.serverID, "agency-1")
	f.staticVersionID = insertStaticVersion(t, pool, f.serverID, f.observed.Add(-24*time.Hour), nil, nil)
	f.boxID = dbtest.ID(t, pool,
		`INSERT INTO bounding_box_version (gtfs_static_version_id, server_id, minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		 VALUES ($1, $2, 47, 48, -123, -122) RETURNING bounding_box_version_id`,
		f.staticVersionID, f.serverID)
	f.responseID = dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
		 VALUES ($1, $2, 'DIFFERENTIAL') RETURNING vehicle_position_response_id`,
		f.endpointID, f.observed)
	return f
}

// insertFullEvidence inserts one evidence record with every value set and
// returns its ID.
func insertFullEvidence(t *testing.T, pool *pgxpool.Pool, f evidenceFixture) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_evidence
		 (vehicle_position_response_id, vehicle_entity_id, feed_entity_id, agency_id, attribution_reason,
		  vehicle_source_time, latitude, longitude, position_valid, reported_speed_mps, computed_speed_mps,
		  speed_discrepancy_ratio, current_status, trip_id, route_id, trip_start_date, trip_start_time_seconds,
		  schedule_relationship, expected_schedule_state, expected_schedule_reason,
		  expected_window_start, expected_window_end, gtfs_static_version_id, bounding_box_version_id)
		 VALUES ($1, $2, 'entity-7', $3, 'ambiguous_identifier',
		         '2026-11-01T09:59:30Z', 47.6, -122.3, true, 12.5, 12.0,
		         1.04, 2, 'trip-9', 'route-44', '2026-11-01', 36000,
		         'SCHEDULED', 'expected_active', 'within_scheduled_window',
		         '2026-11-01T09:50:00Z', '2026-11-01T10:30:00Z', $4, $5)
		 RETURNING vehicle_position_evidence_id`,
		f.responseID, f.vehicleEntityID, f.agencyID, f.staticVersionID, f.boxID)
}

func TestGetVehiclePositionEvidence_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	router := newVehiclePositionEvidenceRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	want := fmt.Sprintf(`{
		"vehicle_position_evidence_id": %d,
		"observed_at": "2026-11-01T10:00:00Z",
		"incrementality": "DIFFERENTIAL",
		"feed_entity_id": "entity-7",
		"agency_id": %d,
		"attribution_reason": "ambiguous_identifier",
		"vehicle_source_time": "2026-11-01T09:59:30Z",
		"latitude": 47.6,
		"longitude": -122.3,
		"position_valid": true,
		"position_validation_reason": null,
		"reported_speed_mps": 12.5,
		"computed_speed_mps": 12,
		"speed_discrepancy_ratio": 1.04,
		"current_status": 2,
		"trip_id": "trip-9",
		"route_id": "route-44",
		"trip_start_date": "2026-11-01",
		"trip_start_time_seconds": 36000,
		"schedule_relationship": "SCHEDULED",
		"expected_schedule_state": "expected_active",
		"expected_schedule_reason": "within_scheduled_window",
		"expected_window_start": "2026-11-01T09:50:00Z",
		"expected_window_end": "2026-11-01T10:30:00Z",
		"links": {
			"self": "/v1/vehicle-position-evidence/%d",
			"response": "/v1/vehicle-position-responses/%d",
			"vehicle": "/v1/vehicles/%d",
			"gtfs_static_version": "/v1/gtfs-static-versions/%d",
			"bounding_box_version": "/v1/bounding-box-versions/%d"
		}
	}`, id, f.agencyID, id, f.responseID, f.vehicleEntityID, f.staticVersionID, f.boxID)
	assertJSONEqual(t, want, rec.Body.Bytes())
}

func TestGetVehiclePositionEvidence_UnknownIDReturns404(t *testing.T) {
	router := newVehiclePositionEvidenceRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, vehiclePositionEvidencePath+"/999999", nil))
}

// TestGetVehiclePositionEvidence_MalformedIDReturns404 checks IDs that are not
// positive integers. The URI names no resource, so each is a 404.
func TestGetVehiclePositionEvidence_MalformedIDReturns404(t *testing.T) {
	router := newVehiclePositionEvidenceRouter(t, dbtest.NewMigratedPool(t))
	for _, id := range []string{"abc", "0", "-1"} {
		rec := doGet(t, router, vehiclePositionEvidencePath+"/"+id, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("ID %q: status = %d, want %d", id, rec.Code, http.StatusNotFound)
		}
	}
}

func TestGetVehiclePositionEvidence_RejectsUnknownParameter(t *testing.T) {
	router := newVehiclePositionEvidenceRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehiclePositionEvidencePath+"/1?foo=1", nil), "foo")
}

func TestGetVehiclePositionEvidence_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	router := newVehiclePositionEvidenceRouter(t, pool)
	path := fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id)

	first := doGet(t, router, path, nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("response has no ETag")
	}
	again := doGet(t, router, path, map[string]string{"If-None-Match": etag})
	if again.Code != http.StatusNotModified {
		t.Errorf("status with If-None-Match = %d, want %d", again.Code, http.StatusNotModified)
	}
	if again.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", again.Body.String())
	}
}

// TestGetVehiclePositionEvidence_Head checks HEAD over a real connection: net/http
// sends the status and headers of GET and no body.
func TestGetVehiclePositionEvidence_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	srv := httptest.NewServer(newVehiclePositionEvidenceRouter(t, pool))
	defer srv.Close()
	path := fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id)

	get, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	head, err := http.Head(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Body.Close()
	if get.StatusCode != http.StatusOK || head.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, HEAD status = %d, want both %d", get.StatusCode, head.StatusCode, http.StatusOK)
	}
	for _, h := range []string{"Cache-Control", "ETag", "Content-Type"} {
		if head.Header.Get(h) != get.Header.Get(h) {
			t.Errorf("HEAD %s = %q, GET %s = %q", h, head.Header.Get(h), h, get.Header.Get(h))
		}
	}
	body, err := io.ReadAll(head.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Errorf("HEAD body has %d bytes, want 0", len(body))
	}
}

// TestGetVehiclePositionEvidence_RawCoordinates stores a latitude outside its range
// and checks that it is returned unchanged, with the validation reason. Null
// fields are still present.
func TestGetVehiclePositionEvidence_RawCoordinates(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_evidence (vehicle_position_response_id, latitude, longitude, position_valid, position_validation_reason)
		 VALUES ($1, 123.4, -122.3, false, 'latitude_out_of_range') RETURNING vehicle_position_evidence_id`,
		f.responseID)
	router := newVehiclePositionEvidenceRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id), nil)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body %s", err, rec.Body)
	}
	if body["latitude"] != 123.4 {
		t.Errorf("latitude = %v, want 123.4", body["latitude"])
	}
	if body["position_valid"] != false || body["position_validation_reason"] != "latitude_out_of_range" {
		t.Errorf("position_valid = %v, reason = %v", body["position_valid"], body["position_validation_reason"])
	}
	for _, key := range []string{"trip_id", "route_id", "trip_start_date", "agency_id", "vehicle_source_time"} {
		v, ok := body[key]
		if !ok {
			t.Errorf("%s is missing; nullable fields must be present", key)
		} else if v != nil {
			t.Errorf("%s = %v, want null", key, v)
		}
	}
	if _, ok := body["links"].(map[string]any)["vehicle"]; ok {
		t.Error("vehicle link present without vehicle_entity_id")
	}
}

// TestGetVehiclePositionEvidence_TripStartDateFormat checks trip_start_date is YYYY-MM-DD.
func TestGetVehiclePositionEvidence_TripStartDateFormat(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	router := newVehiclePositionEvidenceRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id), nil)
	var body struct {
		TripStartDate string `json:"trip_start_date"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TripStartDate != "2026-11-01" {
		t.Errorf("trip_start_date = %q, want 2026-11-01", body.TripStartDate)
	}
}

// TestGetVehiclePositionEvidence_IncludeWithoutLoaderReturns400 checks that a
// link whose target loader does not exist yet cannot be included.
func TestGetVehiclePositionEvidence_IncludeWithoutLoaderReturns400(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	router := newVehiclePositionEvidenceRouter(t, pool)
	for _, name := range []string{"response", "bounding_box_version"} {
		rec := doGet(t, router, fmt.Sprintf("%s/%d?include=%s", vehiclePositionEvidencePath, id, name), nil)
		wantInvalidParam(t, rec, "include")
	}
}

// TestGetVehiclePositionEvidence_CachePolicy checks that the long policy applies
// only without include. Any include gets the short policy.
func TestGetVehiclePositionEvidence_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	id := insertFullEvidence(t, pool, f)
	router := newVehiclePositionEvidenceRouter(t, pool)
	path := fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id)

	for _, tc := range []struct{ path, cacheControl string }{
		{path, "private, max-age=86400"},
		{path + "?include=vehicle", "private, max-age=30"},
	} {
		rec := doGet(t, router, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.cacheControl)
		}
	}
}

// TestVehiclePositionEvidenceLoader loads three records in one call. Each value
// must equal this endpoint's body for that ID.
func TestVehiclePositionEvidenceLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	f := newEvidenceFixture(t, pool)
	// A response holds at most one evidence row, so each record gets its own.
	var ids []int64
	for range 3 {
		g := f
		g.responseID = dbtest.ID(t, pool,
			`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
			 VALUES ($1, $2, 'DIFFERENTIAL') RETURNING vehicle_position_response_id`,
			f.endpointID, f.observed)
		ids = append(ids, insertFullEvidence(t, pool, g))
	}
	router := newVehiclePositionEvidenceRouter(t, pool)

	reps, err := loadVehiclePositionEvidence(context.Background(), dbgen.New(pool), ids)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(reps) != len(ids) {
		t.Fatalf("loader returned %d values, want %d", len(reps), len(ids))
	}
	for _, id := range ids {
		rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionEvidencePath, id), nil)
		raw, err := json.Marshal(reps[id])
		if err != nil {
			t.Fatal(err)
		}
		assertJSONEqual(t, rec.Body.String(), raw)
	}
}

// assertJSONEqual fails unless the two JSON documents have the same value.
func assertJSONEqual(t *testing.T, want string, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v; body %s", err, got)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}
