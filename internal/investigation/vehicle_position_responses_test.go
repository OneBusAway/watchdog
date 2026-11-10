package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newResponsesRouter returns a router serving getVehiclePositionResponse and
// listServerVehiclePositionEndpoints for include comparisons.
func newResponsesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool

	router := httprouter.New()
	router.GET("/v1/vehicle-position-responses/:vehicle_position_response_id", s.getVehiclePositionResponse)
	router.HEAD("/v1/vehicle-position-responses/:vehicle_position_response_id", s.getVehiclePositionResponse)
	return router
}

// insertResponse inserts a vehicle_position_response row and returns its ID.
func insertResponse(t *testing.T, pool *pgxpool.Pool, endpointID int64) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
		 VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id`,
		endpointID, time.Now())
}

func responsePath(id int64) string {
	return "/v1/vehicle-position-responses/" + strconv.FormatInt(id, 10)
}

func TestGetVehiclePositionResponse_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-position-responses/1"); h == nil {
			t.Errorf("%s /v1/vehicle-position-responses/:vehicle_position_response_id is not registered", method)
		}
	}
}

func TestGetVehiclePositionResponse_MalformedIDReturns404(t *testing.T) {
	router := newResponsesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/vehicle-position-responses/"+id, nil))
		})
	}
}

func TestGetVehiclePositionResponse_RejectsUnknownParameter(t *testing.T) {
	router := newResponsesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/vehicle-position-responses/1?foo=1", nil), "foo")
}

func TestGetVehiclePositionResponse_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newResponsesRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/vehicle-position-responses/999999", nil))
}

func TestGetVehiclePositionResponse_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	responseID := insertResponse(t, pool, endpointID)
	router := newResponsesRouter(t, pool)
	path := responsePath(responseID)

	rec := doGet(t, router, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body vehiclePositionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.VehiclePositionResponseID != responseID {
		t.Errorf("vehicle_position_response_id = %d, want %d", body.VehiclePositionResponseID, responseID)
	}
	if body.EndpointID != endpointID {
		t.Errorf("endpoint_id = %d, want %d", body.EndpointID, endpointID)
	}
	if body.Incrementality != "FULL_DATASET" {
		t.Errorf("incrementality = %q, want FULL_DATASET", body.Incrementality)
	}
	if body.Links["self"] != path {
		t.Errorf("self link = %q, want %q", body.Links["self"], path)
	}
	if body.Links["endpoint"] != "/v1/vehicle-position-endpoints/"+strconv.FormatInt(endpointID, 10) {
		t.Errorf("endpoint link = %q", body.Links["endpoint"])
	}
	wantEvidence := "/v1/vehicle-position-evidence?response_id=" + strconv.FormatInt(responseID, 10)
	if body.Links["evidence"] != wantEvidence {
		t.Errorf("evidence link = %q, want %q", body.Links["evidence"], wantEvidence)
	}
	// Immutable cache.
	if got := rec.Header().Get("Cache-Control"); got == "private, max-age=30" {
		t.Error("vehicle-position-response should use immutable cache, not short cache")
	}
}

func TestGetVehiclePositionResponse_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	responseID := insertResponse(t, pool, endpointID)
	router := newResponsesRouter(t, pool)
	path := responsePath(responseID)

	first := doGet(t, router, path, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d", first.Code)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, path, map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", second.Code)
	}
}

func TestGetVehiclePositionResponse_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	responseID := insertResponse(t, pool, endpointID)
	router := newResponsesRouter(t, pool)
	path := responsePath(responseID)

	get := doGet(t, router, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers != GET headers")
	}
}

func TestVehicleResponseLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	ids := []int64{
		insertResponse(t, pool, endpointID),
		insertResponse(t, pool, endpointID),
	}
	insertResponse(t, pool, endpointID) // not asked

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindVehicleResponse](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("loadResponses: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != 2 {
		t.Fatalf("got %d representations, want 2", len(got))
	}
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			t.Errorf("missing representation for id %d", id)
		}
	}
}
