package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

const vehiclePositionResponsesPath = "/v1/vehicle-position-responses"

// responseSHA is a valid payload_sha256 value: 64 lowercase hex characters.
var responseSHA = strings.Repeat("a", 64)

// newVehiclePositionResponsesRouter mounts E28 for GET and HEAD on a service using pool.
// newTestService registers every loader, so include resolves real loaders.
func newVehiclePositionResponsesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET(vehiclePositionResponsesPath+"/:response_id", s.getVehiclePositionResponse)
	router.HEAD(vehiclePositionResponsesPath+"/:response_id", s.getVehiclePositionResponse)
	return router
}

// insertResponseFull inserts one feed response with every value set and returns
// its ID and its endpoint's ID.
func insertResponseFull(t *testing.T, pool *pgxpool.Pool) (responseID, endpointID int64) {
	t.Helper()
	serverID := insertVehicleServer(t, pool, "https://oba.example.org")
	endpointID = insertVehicleEndpoint(t, pool, serverID, "https://oba.example.org/vehicles.pb")
	responseID = dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_response (endpoint_id, observed_at, source_header_time, incrementality, payload_sha256)
		 VALUES ($1, '2026-11-01T10:00:00Z', '2026-11-01T09:59:59Z', 'FULL_DATASET', $2)
		 RETURNING vehicle_position_response_id`,
		endpointID, responseSHA)
	return responseID, endpointID
}

func TestGetVehiclePositionResponse_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, endpointID := insertResponseFull(t, pool)
	router := newVehiclePositionResponsesRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	want := fmt.Sprintf(`{
		"vehicle_position_response_id": %d,
		"endpoint_id": %d,
		"observed_at": "2026-11-01T10:00:00Z",
		"source_header_time": "2026-11-01T09:59:59Z",
		"incrementality": "FULL_DATASET",
		"payload_sha256": %q,
		"links": {
			"self": "/v1/vehicle-position-responses/%d",
			"endpoint": "/v1/vehicle-position-endpoints/%d"
		}
	}`, id, endpointID, responseSHA, id, endpointID)
	assertJSONEqual(t, want, rec.Body.Bytes())
}

// TestGetVehiclePositionResponse_NullableFieldsArePresent checks that a response
// with no header time and no payload hash returns both as null.
func TestGetVehiclePositionResponse_NullableFieldsArePresent(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://oba.example.org")
	endpointID := insertVehicleEndpoint(t, pool, serverID, "https://oba.example.org/vehicles.pb")
	id := dbtest.ID(t, pool,
		`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
		 VALUES ($1, '2026-11-01T10:00:00Z', 'DIFFERENTIAL') RETURNING vehicle_position_response_id`,
		endpointID)
	router := newVehiclePositionResponsesRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id), nil)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body %s", err, rec.Body)
	}
	for _, key := range []string{"source_header_time", "payload_sha256"} {
		v, ok := body[key]
		if !ok {
			t.Errorf("%s is missing; nullable fields must be present", key)
		} else if v != nil {
			t.Errorf("%s = %v, want null", key, v)
		}
	}
}

func TestGetVehiclePositionResponse_UnknownIDReturns404(t *testing.T) {
	router := newVehiclePositionResponsesRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, vehiclePositionResponsesPath+"/999999", nil))
}

// TestGetVehiclePositionResponse_MalformedIDReturns404 checks IDs that are not
// positive integers. The URI names no resource, so each is a 404.
func TestGetVehiclePositionResponse_MalformedIDReturns404(t *testing.T) {
	router := newVehiclePositionResponsesRouter(t, dbtest.NewMigratedPool(t))
	for _, id := range []string{"abc", "0", "-1"} {
		rec := doGet(t, router, vehiclePositionResponsesPath+"/"+id, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("ID %q: status = %d, want %d", id, rec.Code, http.StatusNotFound)
		}
	}
}

func TestGetVehiclePositionResponse_RejectsUnknownParameter(t *testing.T) {
	router := newVehiclePositionResponsesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehiclePositionResponsesPath+"/1?foo=1", nil), "foo")
}

func TestGetVehiclePositionResponse_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, _ := insertResponseFull(t, pool)
	router := newVehiclePositionResponsesRouter(t, pool)
	path := fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id)

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

// TestGetVehiclePositionResponse_Head checks HEAD over a real connection: net/http
// sends the status and headers of GET and no body.
func TestGetVehiclePositionResponse_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, _ := insertResponseFull(t, pool)
	srv := httptest.NewServer(newVehiclePositionResponsesRouter(t, pool))
	defer srv.Close()
	path := fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id)

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

// TestGetVehiclePositionResponse_CacheControl checks the policy decided on #412:
// private for a day without include, and the short policy with any include.
func TestGetVehiclePositionResponse_CacheControl(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, _ := insertResponseFull(t, pool)
	router := newVehiclePositionResponsesRouter(t, pool)
	path := fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id)

	for _, tc := range []struct{ path, cacheControl string }{
		{path, "private, max-age=86400"},
		{path + "?include=endpoint", "private, max-age=30"},
	} {
		rec := doGet(t, router, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.cacheControl)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no ETag", tc.path)
		}
		if got := rec.Header().Get("Last-Modified"); got != "" {
			t.Errorf("%s: Last-Modified = %q, want none", tc.path, got)
		}
	}
}

// TestGetVehiclePositionResponse_IncludeEndpoint checks that endpoint can be
// included, and that the included object is keyed by the endpoint's URL.
func TestGetVehiclePositionResponse_IncludeEndpoint(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, endpointID := insertResponseFull(t, pool)
	router := newVehiclePositionResponsesRouter(t, pool)

	rec := doGet(t, router, fmt.Sprintf("%s/%d?include=endpoint", vehiclePositionResponsesPath, id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]map[string]any `json:"included"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	endpointURL := fmt.Sprintf("/v1/vehicle-position-endpoints/%d", endpointID)
	if _, ok := body.Included[endpointURL]; !ok {
		t.Errorf("included has no %s; keys = %v", endpointURL, body.Included)
	}
}

// TestGetVehiclePositionResponse_Pipeline checks the codes the shared pipeline
// gives this endpoint, through the full router: 401 without or with a wrong
// token, 405 for POST, 406 when JSON is not accepted, 200 when authorized, and
// 503 when the database is disabled.
func TestGetVehiclePositionResponse_Pipeline(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id, _ := insertResponseFull(t, pool)
	s, router := newTestService(t)
	s.Pool = pool
	path := fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id)

	serve := func(handler http.Handler, method string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	auth := map[string]string{"Authorization": "Bearer " + testToken}

	t.Run("no token", func(t *testing.T) {
		rec := serve(router, http.MethodGet, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="watchdog-investigation"` {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		rec := serve(router, http.MethodGet, map[string]string{"Authorization": "Bearer wrong"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("POST", func(t *testing.T) {
		rec := serve(router, http.MethodPost, auth)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
			t.Errorf("Allow = %q, want it to list GET", allow)
		}
	})
	t.Run("Accept without JSON", func(t *testing.T) {
		rec := serve(router, http.MethodGet, map[string]string{"Authorization": "Bearer " + testToken, "Accept": "text/xml"})
		if rec.Code != http.StatusNotAcceptable {
			t.Errorf("status = %d, want 406", rec.Code)
		}
	})
	t.Run("authorized GET", func(t *testing.T) {
		rec := serve(router, http.MethodGet, auth)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body %s", rec.Code, rec.Body)
		}
	})
	t.Run("database disabled", func(t *testing.T) {
		// newTestService has no pool, so the database is disabled.
		_, disabledRouter := newTestService(t)
		rec := serve(disabledRouter, http.MethodGet, auth)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "Investigation storage disabled") {
			t.Errorf("body = %s, want the storage-disabled problem", rec.Body)
		}
	})
}

// responseStatementRecorder records the SQL of every query a pool runs.
type responseStatementRecorder struct {
	mu  sync.Mutex
	sql []string
}

func (q *responseStatementRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sql = append(q.sql, data.SQL)
	return ctx
}

func (q *responseStatementRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// countWith returns how many recorded statements contain sub.
func (q *responseStatementRecorder) countWith(sub string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, stmt := range q.sql {
		if strings.Contains(stmt, sub) {
			n++
		}
	}
	return n
}

// TestVehiclePositionResponseLoader loads three responses in one call. Each value
// must equal this endpoint's body for that ID, and the call runs one query.
func TestVehiclePositionResponseLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://oba.example.org")
	endpointID := insertVehicleEndpoint(t, pool, serverID, "https://oba.example.org/vehicles.pb")
	var ids []int64
	for range 3 {
		ids = append(ids, dbtest.ID(t, pool,
			`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
			 VALUES ($1, '2026-11-01T10:00:00Z', 'FULL_DATASET') RETURNING vehicle_position_response_id`,
			endpointID))
	}
	router := newVehiclePositionResponsesRouter(t, pool)

	rec := &responseStatementRecorder{}
	cfg := pool.Config().Copy()
	cfg.ConnConfig.Tracer = rec
	traced, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(traced.Close)

	reps, err := loadVehiclePositionResponses(context.Background(), dbgen.New(traced), ids)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(reps) != len(ids) {
		t.Fatalf("loader returned %d values, want %d", len(reps), len(ids))
	}
	if n := rec.countWith("vehicle_position_response_id = ANY"); n != 1 {
		t.Errorf("loader ran %d queries for %d IDs, want 1", n, len(ids))
	}
	for _, id := range ids {
		got := doGet(t, router, fmt.Sprintf("%s/%d", vehiclePositionResponsesPath, id), nil)
		raw, err := json.Marshal(reps[id])
		if err != nil {
			t.Fatal(err)
		}
		assertJSONEqual(t, got.Body.String(), raw)
	}
}
