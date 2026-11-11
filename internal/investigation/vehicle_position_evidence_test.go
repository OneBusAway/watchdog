package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newEvidenceRouter returns a router serving getVehiclePositionEvidence on a
// service using pool (nil for tests that never reach the database). Mounts
// directly without the request pipeline, bypassing authenticate until #197.
func newEvidenceRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return nil }

	router := httprouter.New()
	router.GET("/v1/vehicle-position-evidence/:evidence_id", s.getVehiclePositionEvidence)
	router.HEAD("/v1/vehicle-position-evidence/:evidence_id", s.getVehiclePositionEvidence)
	return router
}

// insertResponse records a vehicle_position_response row and returns its ID.
func insertResponse(t *testing.T, pool *pgxpool.Pool, endpointID int64, observedAt time.Time) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
		endpointID, observedAt)
}

// insertEvidence records a vehicle_position_evidence row and returns its ID.
// Uses position_valid = true; the check constraint requires a reason when false.
func insertEvidence(t *testing.T, pool *pgxpool.Pool, responseID int64) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_position_evidence (vehicle_position_response_id, position_valid) VALUES ($1, true) RETURNING vehicle_position_evidence_id",
		responseID)
}

func evidencePath(id int64) string {
	return "/v1/vehicle-position-evidence/" + strconv.FormatInt(id, 10)
}

func TestGetVehiclePositionEvidence_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-position-evidence/1"); h == nil {
			t.Errorf("%s /v1/vehicle-position-evidence/:evidence_id is not registered", method)
		}
	}
}

func TestGetVehiclePositionEvidence_RejectsUnknownParameter(t *testing.T) {
	router := newEvidenceRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/vehicle-position-evidence/1?foo=bar", nil), "foo")
}

func TestGetVehiclePositionEvidence_RejectsBadInclude(t *testing.T) {
	router := newEvidenceRouter(t, nil)
	for _, query := range []string{"include=self", "include=foo", "include=", "include=agency,agency", "include=agency,"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/vehicle-position-evidence/1?"+query, nil), "include")
		})
	}
}

func TestGetVehiclePositionEvidence_MalformedIDReturns404(t *testing.T) {
	router := newEvidenceRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/vehicle-position-evidence/"+id, nil))
		})
	}
}

func TestGetVehiclePositionEvidence_UnknownID(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	router := newEvidenceRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/vehicle-position-evidence/999999", nil))
}

func TestGetVehiclePositionEvidence_ReturnsRepresentation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)

	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	observedAt := time.Date(2026, 10, 24, 15, 10, 30, 0, time.UTC)
	responseID := insertResponse(t, pool, endpointID, observedAt)
	evidenceID := insertEvidence(t, pool, responseID)
	router := newEvidenceRouter(t, pool)

	rec := doGet(t, router, evidencePath(evidenceID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		VehiclePositionEvidenceID int64             `json:"vehicle_position_evidence_id"`
		VehiclePositionResponseID int64             `json:"vehicle_position_response_id"`
		ObservedAt                string            `json:"observed_at"`
		PositionValid             bool              `json:"position_valid"`
		Links                     map[string]string `json:"links"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.VehiclePositionEvidenceID != evidenceID {
		t.Errorf("vehicle_position_evidence_id = %d, want %d", body.VehiclePositionEvidenceID, evidenceID)
	}
	if body.VehiclePositionResponseID != responseID {
		t.Errorf("vehicle_position_response_id = %d, want %d", body.VehiclePositionResponseID, responseID)
	}
	if body.ObservedAt != "2026-10-24T15:10:30Z" {
		t.Errorf("observed_at = %q, want 2026-10-24T15:10:30Z", body.ObservedAt)
	}
	if !body.PositionValid {
		t.Error("position_valid = false, want true")
	}
	wantSelf := evidencePath(evidenceID)
	if got := body.Links["self"]; got != wantSelf {
		t.Errorf("links.self = %q, want %q", got, wantSelf)
	}
	wantResponse := "/v1/vehicle-position-responses/" + strconv.FormatInt(responseID, 10)
	if got := body.Links["response"]; got != wantResponse {
		t.Errorf("links.response = %q, want %q", got, wantResponse)
	}
	wantEndpoint := "/v1/vehicle-position-endpoints/" + strconv.FormatInt(endpointID, 10)
	if got := body.Links["endpoint"]; got != wantEndpoint {
		t.Errorf("links.endpoint = %q, want %q", got, wantEndpoint)
	}
}

func TestGetVehiclePositionEvidence_CacheImmutable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)

	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	observedAt := time.Date(2026, 10, 24, 15, 10, 30, 0, time.UTC)
	responseID := insertResponse(t, pool, endpointID, observedAt)
	evidenceID := insertEvidence(t, pool, responseID)
	router := newEvidenceRouter(t, pool)

	rec := doGet(t, router, evidencePath(evidenceID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	if got := rec.Header().Get("Last-Modified"); got == "" {
		t.Error("no Last-Modified header")
	}
}

func TestGetVehiclePositionEvidence_Head(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)

	serverID := insertServer(t, pool, "https://example.com", "Example")
	endpointID := insertEndpoint(t, pool, serverID, "https://feeds.example.com/vp")
	responseID := insertResponse(t, pool, endpointID, time.Now())
	evidenceID := insertEvidence(t, pool, responseID)
	router := newEvidenceRouter(t, pool)
	path := evidencePath(evidenceID)

	get := doGet(t, router, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
}
