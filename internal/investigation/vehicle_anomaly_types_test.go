package investigation

import (
	"context"
	"encoding/json"
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

const vehicleAnomalyTypesPath = "/v1/vehicle-anomaly-types"

// newVehicleAnomalyTypesRouter mounts E30 and E31 for GET and HEAD on a service using pool.
func newVehicleAnomalyTypesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET(vehicleAnomalyTypesPath, s.listVehicleAnomalyTypes)
	router.HEAD(vehicleAnomalyTypesPath, s.listVehicleAnomalyTypes)
	router.GET(vehicleAnomalyTypesPath+"/:anomaly_code", s.getVehicleAnomalyType)
	router.HEAD(vehicleAnomalyTypesPath+"/:anomaly_code", s.getVehicleAnomalyType)
	return router
}

func TestListVehicleAnomalyTypes_RejectsUnknownParameter(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyTypesPath+"?foo=1", nil), "foo")
}

func TestListVehicleAnomalyTypes_Head(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	get := doGet(t, router, vehicleAnomalyTypesPath, nil)
	head := headRequest(t, router, vehicleAnomalyTypesPath)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", get.Code, http.StatusOK)
	}
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", head.Code, http.StatusOK)
	}
	for _, rec := range []struct {
		name string
		code int
		h    http.Header
	}{{"GET", get.Code, get.Header()}, {"HEAD", head.Code, head.Header()}} {
		if got := rec.h.Get("Cache-Control"); got != "private, max-age=30" {
			t.Errorf("%s Cache-Control = %q, want %q", rec.name, got, "private, max-age=30")
		}
		for _, h := range []string{"ETag", "Content-Type"} {
			if rec.h.Get(h) == "" {
				t.Errorf("%s %s is empty", rec.name, h)
			}
		}
	}
	for _, h := range []string{"Cache-Control", "ETag", "Content-Type"} {
		if head.Header().Get(h) != get.Header().Get(h) {
			t.Errorf("HEAD %s = %q, GET %s = %q", h, head.Header().Get(h), h, get.Header().Get(h))
		}
	}
	// net/http drops a HEAD body itself, so the body is not compared here.
}

// TestListVehicleAnomalyTypes_Seeded checks the three types the migration seeds,
// in anomaly_code order, with their links.
func TestListVehicleAnomalyTypes_Seeded(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	body := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyTypesPath, nil))

	item := func(code, description string) map[string]any {
		return map[string]any{
			"anomaly_code": code,
			"description":  description,
			"links": map[string]any{
				"self":          vehicleAnomalyTypesPath + "/" + code,
				"rule_versions": vehicleAnomalyTypesPath + "/" + code + "/rule-versions",
			},
		}
	}
	want := map[string]any{
		"items": []any{
			item("invalid_coordinate", "The vehicle position has coordinates that fail validation."),
			item("stale_source_timestamp", "The vehicle's GTFS-Realtime source timestamp is older than the stale threshold."),
			item("stopped_out_of_bounds", "The vehicle is STOPPED_AT a location outside the applicable bounding box."),
		},
		"links": map[string]any{"self": vehicleAnomalyTypesPath},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body =\n%v\nwant\n%v", body, want)
	}
}

const staleSourceTypePath = vehicleAnomalyTypesPath + "/stale_source_timestamp"

// TestGetVehicleAnomalyType_Seeded checks one seeded type by its code (E31).
func TestGetVehicleAnomalyType_Seeded(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	body := decodeEpisodeBody(t, doGet(t, router, staleSourceTypePath, nil))
	want := map[string]any{
		"anomaly_code": "stale_source_timestamp",
		"description":  "The vehicle's GTFS-Realtime source timestamp is older than the stale threshold.",
		"links": map[string]any{
			"self":          staleSourceTypePath,
			"rule_versions": staleSourceTypePath + "/rule-versions",
		},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body =\n%v\nwant\n%v", body, want)
	}
}

// TestGetVehicleAnomalyType_ReturnsRepresentation inserts its own type and
// checks every field and link of the response.
func TestGetVehicleAnomalyType_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool, `INSERT INTO vehicle_anomaly_type (anomaly_code, description) VALUES ('test_type', 'A test type.')`); err != nil {
		t.Fatal(err)
	}
	router := newVehicleAnomalyTypesRouter(t, pool)
	rec := doGet(t, router, vehicleAnomalyTypesPath+"/test_type", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got struct {
		AnomalyCode string            `json:"anomaly_code"`
		Description string            `json:"description"`
		Links       map[string]string `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AnomalyCode != "test_type" || got.Description != "A test type." {
		t.Errorf("anomaly_code, description = %q, %q", got.AnomalyCode, got.Description)
	}
	wantLinks := map[string]string{
		"self":          vehicleAnomalyTypesPath + "/test_type",
		"rule_versions": vehicleAnomalyTypesPath + "/test_type/rule-versions",
	}
	if !reflect.DeepEqual(got.Links, wantLinks) {
		t.Errorf("links = %v, want %v", got.Links, wantLinks)
	}
}

func TestGetVehicleAnomalyType_UnknownCodeIsNotFound(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	rec := doGet(t, router, vehicleAnomalyTypesPath+"/no_such_type", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var problem struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v; body %s", err, rec.Body)
	}
	if problem.Title != "Not found" {
		t.Errorf("title = %q, want %q", problem.Title, "Not found")
	}
}

// TestGetVehicleAnomalyType_MalformedIDReturns404 checks codes that cannot name
// a type. They are not numeric IDs, so they match no row.
func TestGetVehicleAnomalyType_MalformedIDReturns404(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	for _, code := range []string{"abc", "0", "-1"} {
		rec := doGet(t, router, vehicleAnomalyTypesPath+"/"+code, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("code %q: status = %d, want %d", code, rec.Code, http.StatusNotFound)
		}
	}
}

func TestGetVehicleAnomalyType_RejectsUnknownParameter(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, staleSourceTypePath+"?foo=1", nil), "foo")
}

func TestGetVehicleAnomalyType_ETag(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	first := doGet(t, router, staleSourceTypePath, nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("response has no ETag")
	}
	again := doGet(t, router, staleSourceTypePath, map[string]string{"If-None-Match": etag})
	if again.Code != http.StatusNotModified {
		t.Errorf("status with If-None-Match = %d, want %d", again.Code, http.StatusNotModified)
	}
	if again.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", again.Body.String())
	}
}

// TestGetVehicleAnomalyType_Head checks HEAD over a real connection: net/http
// sends the status and headers of GET and no body.
func TestGetVehicleAnomalyType_Head(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	srv := httptest.NewServer(router)
	defer srv.Close()

	get, err := http.Get(srv.URL + staleSourceTypePath)
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	head, err := http.Head(srv.URL + staleSourceTypePath)
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

// TestAnomalyTypeLoader loads three types by anomaly_type_id in one call. Each
// value must equal the endpoint's representation for that code.
func TestAnomalyTypeLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	codes := []string{"invalid_coordinate", "stale_source_timestamp", "stopped_out_of_bounds"}
	var ids []int64
	for _, code := range codes {
		ids = append(ids, dbtest.ID(t, pool, `SELECT anomaly_type_id::bigint FROM vehicle_anomaly_type WHERE anomaly_code = $1`, code))
	}
	q := dbgen.New(pool)
	reps, err := loadVehicleAnomalyTypes(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(reps) != len(codes) {
		t.Fatalf("loader returned %d values, want %d", len(reps), len(codes))
	}
	router := newVehicleAnomalyTypesRouter(t, pool)
	for i, code := range codes {
		rec := doGet(t, router, vehicleAnomalyTypesPath+"/"+code, nil)
		var endpoint map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &endpoint); err != nil {
			t.Fatalf("decode %s: %v", code, err)
		}
		// The loader's value is the same struct the endpoint encodes.
		raw, err := json.Marshal(reps[ids[i]])
		if err != nil {
			t.Fatal(err)
		}
		var fromLoader map[string]any
		if err := json.Unmarshal(raw, &fromLoader); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromLoader, endpoint) {
			t.Errorf("%s: loader = %v, endpoint = %v", code, fromLoader, endpoint)
		}
	}
}

// insertRuleVersion inserts a rule version for the seeded anomaly type code and
// returns its rule_version_id.
func insertRuleVersion(t *testing.T, pool *pgxpool.Pool, code string, logic int32, name string,
	build *string, effectiveAt time.Time, stale *int32, speed *float64) int64 {
	t.Helper()
	return dbtest.ID(t, pool, `INSERT INTO vehicle_anomaly_rule_version
		(anomaly_type_id, rule_logic_version, version_name, watchdog_build_version,
		 effective_at, stale_age_threshold_seconds, reported_speed_threshold_mps)
		SELECT anomaly_type_id, $2, $3, $4, $5, $6, $7 FROM vehicle_anomaly_type WHERE anomaly_code = $1
		RETURNING rule_version_id`, code, logic, name, build, effectiveAt, stale, speed)
}

func TestRuleVersionLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	build := "v1.2.3"
	stale := int32(150)
	speed := 45.5
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ids := []int64{
		insertRuleVersion(t, pool, "stale_source_timestamp", 1, "stale > 150s", &build, base, &stale, nil),
		insertRuleVersion(t, pool, "invalid_coordinate", 1, "logic 1", nil, base.Add(time.Hour), nil, nil),
		insertRuleVersion(t, pool, "stopped_out_of_bounds", 2, "speed cap", nil, base.Add(2*time.Hour), nil, &speed),
	}

	q := dbgen.New(pool)
	loaded, err := loadVehicleAnomalyRuleVersions(context.Background(), q, ids)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(loaded) != len(ids) {
		t.Fatalf("loader returned %d values, want %d", len(loaded), len(ids))
	}
	rows, err := q.GetVehicleAnomalyRuleVersion(context.Background(), ids)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, row := range rows {
		want := vehicleAnomalyRuleVersionRepresentation(row, vehicleAnomalyRuleVersionLinks(row))
		if !reflect.DeepEqual(loaded[row.RuleVersionID], want) {
			t.Errorf("rule version %d: loader = %+v, want %+v", row.RuleVersionID, loaded[row.RuleVersionID], want)
		}
	}
}
