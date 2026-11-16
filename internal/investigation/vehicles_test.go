package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newVehiclesRouter mounts E21 on a bare router. The handler is registered
// directly rather than through s.get, because A1's authenticate rejects every
// request until A2 adds the token check; registration itself is covered by
// TestVehicles_Registered.
func newVehiclesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/vehicles", s.listVehicles)
	router.HEAD("/v1/vehicles", s.listVehicles)
	return router
}

func insertVehicleServer(t *testing.T, pool *pgxpool.Pool, url string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ($1, 'Example') RETURNING server_id", url)
}

func insertVehicleEndpoint(t *testing.T, pool *pgxpool.Pool, serverID int64, url string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_position_endpoint (server_id, normalized_vehicle_position_url) VALUES ($1, $2) RETURNING endpoint_id",
		serverID, url)
}

func insertVehicle(t *testing.T, pool *pgxpool.Pool, endpointID int64, vehicleID string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_entity (endpoint_id, external_vehicle_id) VALUES ($1, $2) RETURNING vehicle_entity_id",
		endpointID, vehicleID)
}

// decodeVehiclesBody decodes a 200 E21 response.
func decodeVehiclesBody(t *testing.T, rec *httptest.ResponseRecorder) vehiclesBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body vehiclesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body %s", err, rec.Body)
	}
	return body
}

// vehicleEntityIDs are the IDs of a response's items, in order.
func vehicleEntityIDs(body vehiclesBody) []int64 {
	ids := make([]int64, 0, len(body.Items))
	for _, item := range body.Items {
		ids = append(ids, item.VehicleEntityID)
	}
	return ids
}

func TestVehicles_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicles"); h == nil {
			t.Errorf("%s /v1/vehicles is not registered", method)
		}
	}
}

func TestListVehicles_RejectsUnknownParameter(t *testing.T) {
	router := newVehiclesRouter(t, nil)
	rec := doGet(t, router, "/v1/vehicles?vehicle_id=1&endpoint_id=1&foo=1", nil)
	wantInvalidParam(t, rec, "foo")
}

func TestListVehicles_RequiresScope(t *testing.T) {
	router := newVehiclesRouter(t, nil)
	for _, query := range []string{
		"/v1/vehicles?vehicle_id=1",
		"/v1/vehicles?vehicle_id=1&endpoint_id=1&server_id=1",
	} {
		if rec := doGet(t, router, query, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body %s", query, rec.Code, rec.Body)
		}
	}
}

func TestListVehicles_RequiresVehicleID(t *testing.T) {
	router := newVehiclesRouter(t, nil)
	// An absent vehicle_id and an empty one are both rejected, so an empty
	// value can never widen the query to every vehicle on the scope.
	for _, query := range []string{
		"/v1/vehicles?endpoint_id=1",
		"/v1/vehicles?endpoint_id=1&vehicle_id=",
	} {
		rec := doGet(t, router, query, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body %s", query, rec.Code, rec.Body)
		}
		wantInvalidParam(t, rec, "vehicle_id")
	}
}

func TestListVehicles_RejectsMalformedScope(t *testing.T) {
	router := newVehiclesRouter(t, nil)
	for _, query := range []string{
		"/v1/vehicles?vehicle_id=8049&endpoint_id=abc",
		"/v1/vehicles?vehicle_id=8049&endpoint_id=0",
		"/v1/vehicles?vehicle_id=8049&server_id=-1",
	} {
		if rec := doGet(t, router, query, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body %s", query, rec.Code, rec.Body)
		}
	}
}

func TestListVehicles_ByEndpoint(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://example.com/api")
	first := insertVehicleEndpoint(t, pool, serverID, "https://example.com/first.pb")
	second := insertVehicleEndpoint(t, pool, serverID, "https://example.com/second.pb")
	wanted := insertVehicle(t, pool, first, "8049")
	insertVehicle(t, pool, second, "8049")

	router := newVehiclesRouter(t, pool)
	body := decodeVehiclesBody(t, doGet(t, router, "/v1/vehicles?vehicle_id=8049&endpoint_id="+strconv.FormatInt(first, 10), nil))
	if got := vehicleEntityIDs(body); len(got) != 1 || got[0] != wanted {
		t.Fatalf("ids = %v, want [%d]", got, wanted)
	}
	item := body.Items[0]
	if item.EndpointID != first {
		t.Errorf("endpoint_id = %d, want %d", item.EndpointID, first)
	}
	if item.VehicleID != "8049" {
		t.Errorf("vehicle_id = %q, want %q", item.VehicleID, "8049")
	}
	self := "/v1/vehicles/" + strconv.FormatInt(wanted, 10)
	want := links{
		"self":             self,
		"endpoint":         "/v1/vehicle-position-endpoints/" + strconv.FormatInt(first, 10),
		"anomaly_episodes": self + "/anomaly-episodes",
	}
	for name, url := range want {
		if item.Links[name] != url {
			t.Errorf("links[%q] = %q, want %q", name, item.Links[name], url)
		}
	}
	if len(item.Links) != len(want) {
		t.Errorf("links = %v, want exactly %v", item.Links, want)
	}
}

func TestListVehicles_ByServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://example.com/api")
	first := insertVehicleEndpoint(t, pool, serverID, "https://example.com/first.pb")
	second := insertVehicleEndpoint(t, pool, serverID, "https://example.com/second.pb")
	one := insertVehicle(t, pool, first, "8049")
	two := insertVehicle(t, pool, second, "8049")

	other := insertVehicleServer(t, pool, "https://other.example.com/api")
	otherEndpoint := insertVehicleEndpoint(t, pool, other, "https://other.example.com/vp.pb")
	insertVehicle(t, pool, otherEndpoint, "8049")

	router := newVehiclesRouter(t, pool)
	body := decodeVehiclesBody(t, doGet(t, router, "/v1/vehicles?vehicle_id=8049&server_id="+strconv.FormatInt(serverID, 10), nil))
	got := vehicleEntityIDs(body)
	if len(got) != 2 || got[0] != one || got[1] != two {
		t.Fatalf("ids = %v, want [%d %d]", got, one, two)
	}
}

func TestListVehicles_NoMatchReturnsEmptyItems(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://example.com/api")
	endpointID := insertVehicleEndpoint(t, pool, serverID, "https://example.com/vp.pb")

	router := newVehiclesRouter(t, pool)
	rec := doGet(t, router, "/v1/vehicles?vehicle_id=nosuch&endpoint_id="+strconv.FormatInt(endpointID, 10), nil)
	if body := decodeVehiclesBody(t, rec); len(body.Items) != 0 {
		t.Errorf("items = %v, want none", body.Items)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"items":[]`) {
		t.Errorf(`items must encode as [] rather than null; got %s`, got)
	}
}

func TestListVehicles_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertVehicleServer(t, pool, "https://example.com/api")
	endpointID := insertVehicleEndpoint(t, pool, serverID, "https://example.com/vp.pb")
	insertVehicle(t, pool, endpointID, "8049")

	router := newVehiclesRouter(t, pool)
	path := "/v1/vehicles?vehicle_id=8049&endpoint_id=" + strconv.FormatInt(endpointID, 10)
	get := doGet(t, router, path, nil)

	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	// net/http drops a HEAD body itself, so only the headers are compared here.
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

// TestVehicleItems_Representation pins the exact fields and links of one item,
// without a database.
func TestVehicleItems_Representation(t *testing.T) {
	items, _ := vehicleItems([]dbgen.VehicleEntity{{
		VehicleEntityID:   7,
		EndpointID:        3,
		ExternalVehicleID: "8049",
	}})
	data, err := encodeJSON(items)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := `[{"vehicle_entity_id":7,"endpoint_id":3,"vehicle_id":"8049","links":{` +
		`"anomaly_episodes":"/v1/vehicles/7/anomaly-episodes",` +
		`"endpoint":"/v1/vehicle-position-endpoints/3",` +
		`"self":"/v1/vehicles/7"}}]` + "\n"
	if string(data) != want {
		t.Errorf("items =\n%s\nwant\n%s", data, want)
	}

	if data, _ := encodeJSON(func() []vehicle { items, _ := vehicleItems(nil); return items }()); string(data) != "[]\n" {
		t.Errorf("no rows encode as %s, want []", data)
	}
}

func TestListVehicles_IncludeEndpointRejectedWithoutLoader(t *testing.T) {
	router := newVehiclesRouter(t, nil)
	rec := doGet(t, router, "/v1/vehicles?vehicle_id=1&endpoint_id=1&include=endpoint", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
}
