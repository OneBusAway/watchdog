package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newServersRouter returns a router serving listServers for GET and HEAD on a
// service using pool (which may be nil for tests that never reach the
// database) and configured as its current configuration.
//
// The handler is mounted without the request pipeline: until #197 replaces
// the authenticate stub, every request through s.get is rejected with 401.
// The pipeline's own checks (401, 405, 406, 503) are tested in routes_test.go
// and pipeline_test.go.
func newServersRouter(t *testing.T, pool *pgxpool.Pool, configured ...string) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	servers := make([]models.ObaServer, 0, len(configured))
	for _, u := range configured {
		servers = append(servers, models.ObaServer{ObaBaseURL: u})
	}
	s.Servers = func() []models.ObaServer { return servers }

	router := httprouter.New()
	router.GET("/v1/servers", s.listServers)
	router.HEAD("/v1/servers", s.listServers)
	return router
}

// insertServer records a server and returns its ID.
func insertServer(t *testing.T, pool *pgxpool.Pool, baseURL, name string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ($1, $2) RETURNING server_id",
		baseURL, name)
}

// decodeServers decodes a 200 E02 body.
func decodeServers(t *testing.T, rec *httptest.ResponseRecorder) serverListBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body serverListBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// serverListBody is the E02 body as a client decodes it.
type serverListBody struct {
	Items []struct {
		ServerID          int64             `json:"server_id"`
		NormalizedBaseURL string            `json:"normalized_base_url"`
		Configured        bool              `json:"configured"`
		Links             map[string]string `json:"links"`
	} `json:"items"`
	Links map[string]string `json:"links"`
}

// wantInvalidParam checks rec is a 400 whose invalid_params names exactly name.
func wantInvalidParam(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var body problem
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.InvalidParams) != 1 || body.InvalidParams[0].Name != name {
		t.Errorf("invalid_params = %+v, want one entry naming %q", body.InvalidParams, name)
	}
}

func TestListServers_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/servers"); h == nil {
			t.Errorf("%s /v1/servers is not registered", method)
		}
	}
}

func TestListServers_RejectsUnknownParameter(t *testing.T) {
	router := newServersRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers?foo=1", nil), "foo")
}

func TestListServers_RejectsInclude(t *testing.T) {
	router := newServersRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers?include=agencies", nil), "include")
}

func TestListServers_RejectsPaging(t *testing.T) {
	router := newServersRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers?page_size=10", nil), "page_size")
	wantInvalidParam(t, doGet(t, router, "/v1/servers?cursor=abc", nil), "cursor")
}

func TestListServers_BaseURLEmpty(t *testing.T) {
	router := newServersRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers?base_url=", nil), "base_url")
	// A value with no host normalizes to nothing.
	wantInvalidParam(t, doGet(t, router, "/v1/servers?base_url=https://", nil), "base_url")
}

func TestListServers_BaseURLRepeated(t *testing.T) {
	router := newServersRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers?base_url=a.com&base_url=b.com", nil), "base_url")
}

func TestListServers_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertServer(t, pool, "https://example.com/api", "Example")
	router := newServersRouter(t, pool)

	get := doGet(t, router, "/v1/servers", nil)
	req := httptest.NewRequest(http.MethodHead, "/v1/servers", nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
	// httptest.ResponseRecorder keeps what the handler wrote; net/http drops
	// a HEAD body on the wire. The headers matching, Content-Length included,
	// is what this test checks.
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

func TestListServers_OrderedByID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	// Insert out of ID order, so a missing ORDER BY shows.
	for _, row := range []struct {
		id  int64
		url string
	}{{30, "https://c.example.com"}, {10, "https://a.example.com"}, {20, "https://b.example.com"}} {
		if err := dbtest.Exec(pool,
			"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $2)",
			row.id, row.url); err != nil {
			t.Fatalf("insert server %d: %v", row.id, err)
		}
	}
	router := newServersRouter(t, pool)

	body := decodeServers(t, doGet(t, router, "/v1/servers", nil))
	var got []int64
	for _, item := range body.Items {
		got = append(got, item.ServerID)
	}
	if want := []int64{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Errorf("server_ids = %v, want %v", got, want)
	}
	if body.Links["self"] != "/v1/servers" {
		t.Errorf("links.self = %q, want /v1/servers", body.Links["self"])
	}
}

func TestListServers_BaseURLLookup(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertServer(t, pool, "https://other.example.com/api", "Other")
	id := insertServer(t, pool, "https://example.com/api", "Example")
	router := newServersRouter(t, pool)

	// Credentials and the query string are dropped by
	// utils.SanitizeServerURL. It keeps the host's case, so this URL uses a
	// lowercase host.
	body := decodeServers(t, doGet(t, router, "/v1/servers?base_url=https://User:Pass@example.com/api?key%3Dx", nil))
	if len(body.Items) != 1 || body.Items[0].ServerID != id {
		t.Fatalf("items = %+v, want only server %d", body.Items, id)
	}

	rec := doGet(t, router, "/v1/servers?base_url=https://nowhere.example.com", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := string(raw["items"]); got != "[]" {
		t.Errorf("items = %s, want []", got)
	}
}

func TestListServers_Configured(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	monitored := insertServer(t, pool, "https://monitored.example.com", "Monitored")
	retired := insertServer(t, pool, "https://retired.example.com", "Retired")
	// The configuration holds the URL unnormalized, as config.json would.
	router := newServersRouter(t, pool, "https://monitored.example.com?key=secret")

	body := decodeServers(t, doGet(t, router, "/v1/servers", nil))
	got := map[int64]bool{}
	for _, item := range body.Items {
		got[item.ServerID] = item.Configured
	}
	if want := map[int64]bool{monitored: true, retired: false}; !reflect.DeepEqual(got, want) {
		t.Errorf("configured = %v, want %v", got, want)
	}
}

// TestServerItems_Representation checks the exact fields and links of one
// item, without a database.
func TestServerItems_Representation(t *testing.T) {
	created := time.Date(2026, 10, 24, 16, 5, 30, 0, time.FixedZone("+02", 2*60*60))
	rows := []dbgen.OnebusawayServer{{
		ServerID:          7,
		NormalizedBaseUrl: "https://example.com",
		DisplayName:       "Example",
		CreatedAt:         created,
	}}

	data, err := encodeJSON(serverItems(rows, map[string]bool{"https://example.com": true}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := `[{"server_id":7,"normalized_base_url":"https://example.com","display_name":"Example",` +
		`"created_at":"2026-10-24T14:05:30Z","configured":true,"links":{` +
		`"agencies":"/v1/servers/7/agencies",` +
		`"gtfs_static_versions":"/v1/gtfs-static-versions?server_id=7",` +
		`"self":"/v1/servers/7",` +
		`"vehicle_position_endpoints":"/v1/servers/7/vehicle-position-endpoints"}}]` + "\n"
	if string(data) != want {
		t.Errorf("body =\n%s\nwant\n%s", data, want)
	}

	if data, _ := encodeJSON(serverItems(nil, nil)); string(data) != "[]\n" {
		t.Errorf("no rows encode as %s, want []", data)
	}
}
