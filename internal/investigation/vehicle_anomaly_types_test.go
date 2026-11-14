package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newAnomalyTypesRouter returns a router serving listVehicleAnomalyTypes and
// getVehicleAnomalyType on a service using pool (nil for tests that never
// reach the database).
func newAnomalyTypesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool

	router := httprouter.New()
	router.GET("/v1/vehicle-anomaly-types", s.listVehicleAnomalyTypes)
	router.HEAD("/v1/vehicle-anomaly-types", s.listVehicleAnomalyTypes)
	router.GET("/v1/vehicle-anomaly-types/:anomaly_code", s.getVehicleAnomalyType)
	router.HEAD("/v1/vehicle-anomaly-types/:anomaly_code", s.getVehicleAnomalyType)
	return router
}

// insertAnomalyType inserts a row into vehicle_anomaly_type and returns its
// anomaly_type_id.
func insertAnomalyType(t *testing.T, pool *pgxpool.Pool, code, description string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_anomaly_type (anomaly_code, description) VALUES ($1, $2) RETURNING anomaly_type_id",
		code, description)
}

// anomalyTypeListBody is the E30 body as a client decodes it.
type anomalyTypeListBody struct {
	Items []struct {
		AnomalyCode string            `json:"anomaly_code"`
		Description string            `json:"description"`
		Links       map[string]string `json:"links"`
	} `json:"items"`
	Links map[string]string `json:"links"`
}

func TestListVehicleAnomalyTypes_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-anomaly-types"); h == nil {
			t.Errorf("%s /v1/vehicle-anomaly-types is not registered", method)
		}
	}
}

func TestListVehicleAnomalyTypes_RejectsUnknownParameter(t *testing.T) {
	router := newAnomalyTypesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/vehicle-anomaly-types?foo=1", nil), "foo")
}

func TestListVehicleAnomalyTypes_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "test_code", "Test description")
	router := newAnomalyTypesRouter(t, pool)

	get := doGet(t, router, "/v1/vehicle-anomaly-types", nil)
	req := httptest.NewRequest(http.MethodHead, "/v1/vehicle-anomaly-types", nil)
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
	if got := get.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if get.Header().Get("ETag") == "" {
		t.Error("no ETag header")
	}
}

func TestListVehicleAnomalyTypes_ReturnsItems(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "test_alpha", "Alpha anomaly")
	insertAnomalyType(t, pool, "test_beta", "Beta anomaly")
	router := newAnomalyTypesRouter(t, pool)

	rec := doGet(t, router, "/v1/vehicle-anomaly-types", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body anomalyTypeListBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The migration seeds 3 rows; we inserted 2 more.
	if len(body.Items) < 2 {
		t.Fatalf("want at least 2 items, got %d", len(body.Items))
	}
	// Find our test_alpha item.
	var found bool
	for _, item := range body.Items {
		if item.AnomalyCode == "test_alpha" {
			found = true
			if item.Description != "Alpha anomaly" {
				t.Errorf("description = %q, want %q", item.Description, "Alpha anomaly")
			}
			if item.Links["self"] != "/v1/vehicle-anomaly-types/test_alpha" {
				t.Errorf("self link = %q, want /v1/vehicle-anomaly-types/test_alpha", item.Links["self"])
			}
			if item.Links["rule_versions"] != "/v1/vehicle-anomaly-types/test_alpha/rule-versions" {
				t.Errorf("rule_versions link = %q", item.Links["rule_versions"])
			}
		}
	}
	if !found {
		t.Error("test_alpha item not found in response")
	}
}

func TestListVehicleAnomalyTypes_OrderedByCode(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "zzz_last", "Last")
	insertAnomalyType(t, pool, "aaa_first", "First")
	router := newAnomalyTypesRouter(t, pool)

	rec := doGet(t, router, "/v1/vehicle-anomaly-types", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body anomalyTypeListBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// aaa_first must appear before zzz_last.
	var aIdx, zIdx int = -1, -1
	for i, item := range body.Items {
		if item.AnomalyCode == "aaa_first" {
			aIdx = i
		}
		if item.AnomalyCode == "zzz_last" {
			zIdx = i
		}
	}
	if aIdx < 0 || zIdx < 0 {
		t.Fatalf("missing test items: aIdx=%d, zIdx=%d", aIdx, zIdx)
	}
	if aIdx >= zIdx {
		t.Errorf("aaa_first (idx %d) not before zzz_last (idx %d)", aIdx, zIdx)
	}
}

func TestGetVehicleAnomalyType_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-anomaly-types/:anomaly_code"); h == nil {
			t.Errorf("%s /v1/vehicle-anomaly-types/:anomaly_code is not registered", method)
		}
	}
}

func TestGetVehicleAnomalyType_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "test_anomaly", "Test anomaly description")
	router := newAnomalyTypesRouter(t, pool)

	rec := doGet(t, router, "/v1/vehicle-anomaly-types/test_anomaly", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body vehicleAnomalyType
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AnomalyCode != "test_anomaly" {
		t.Errorf("anomaly_code = %q, want %q", body.AnomalyCode, "test_anomaly")
	}
	if body.Description != "Test anomaly description" {
		t.Errorf("description = %q, want %q", body.Description, "Test anomaly description")
	}
	if body.Links["self"] != "/v1/vehicle-anomaly-types/test_anomaly" {
		t.Errorf("self link = %q", body.Links["self"])
	}
	if body.Links["rule_versions"] != "/v1/vehicle-anomaly-types/test_anomaly/rule-versions" {
		t.Errorf("rule_versions link = %q", body.Links["rule_versions"])
	}
}

func TestGetVehicleAnomalyType_UnknownCodeReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newAnomalyTypesRouter(t, pool)
	rec := doGet(t, router, "/v1/vehicle-anomaly-types/no_such_code", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetVehicleAnomalyType_RejectsUnknownParameter(t *testing.T) {
	router := newAnomalyTypesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/vehicle-anomaly-types/stale_source_timestamp?foo=1", nil), "foo")
}

func TestGetVehicleAnomalyType_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "etag_test", "ETag test")
	router := newAnomalyTypesRouter(t, pool)

	get := doGet(t, router, "/v1/vehicle-anomaly-types/etag_test", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("first GET status = %d", get.Code)
	}
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on first response")
	}
	cond := doGet(t, router, "/v1/vehicle-anomaly-types/etag_test", map[string]string{"If-None-Match": etag})
	if cond.Code != http.StatusNotModified {
		t.Errorf("conditional GET status = %d, want 304", cond.Code)
	}
	if cond.Body.Len() != 0 {
		t.Error("304 response must have no body")
	}
}

func TestGetVehicleAnomalyType_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAnomalyType(t, pool, "head_test", "HEAD test")
	router := newAnomalyTypesRouter(t, pool)

	get := doGet(t, router, "/v1/vehicle-anomaly-types/head_test", nil)
	req := httptest.NewRequest(http.MethodHead, "/v1/vehicle-anomaly-types/head_test", nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers != GET headers")
	}
}

func TestAnomalyTypeLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id1 := insertAnomalyType(t, pool, "loader_a", "Loader A")
	id2 := insertAnomalyType(t, pool, "loader_b", "Loader B")
	id3 := insertAnomalyType(t, pool, "loader_c", "Loader C")
	insertAnomalyType(t, pool, "loader_not_asked", "Not asked")

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	ids := []int64{id1, id2, id3}
	got, err := s.loaders[kindAnomalyType](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("loadAnomalyTypes: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != 3 {
		t.Fatalf("got %d representations, want 3", len(got))
	}
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			t.Errorf("missing representation for id %d", id)
		}
	}
}
