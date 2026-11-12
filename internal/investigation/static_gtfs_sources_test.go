package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newStaticGtfsSourcesRouter returns a router serving getStaticGtfsSource for
// GET and HEAD, with the server loader registered so ?include=server works.
//
// Like newServersRouter, it mounts the handlers without the request pipeline.
func newStaticGtfsSourcesRouter(t *testing.T, pool *pgxpool.Pool, servers ...models.ObaServer) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return servers }

	router := httprouter.New()
	router.GET("/v1/static-gtfs-sources/:source_id", s.getStaticGtfsSource)
	router.HEAD("/v1/static-gtfs-sources/:source_id", s.getStaticGtfsSource)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

func TestGetStaticGtfsSource_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/static-gtfs-sources/:source_id"); h == nil {
			t.Errorf("%s /v1/static-gtfs-sources/:source_id is not registered", method)
		}
	}
}

func TestGetStaticGtfsSource_RejectsUnknownParameter(t *testing.T) {
	wantInvalidParam(t, doGet(t, newStaticGtfsSourcesRouter(t, nil), "/v1/static-gtfs-sources/1?foo=bar", nil), "foo")
}

func TestGetStaticGtfsSource_RejectsBadInclude(t *testing.T) {
	for _, query := range []string{"include=self", "include=agency", "include=", "include=server,server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, newStaticGtfsSourcesRouter(t, nil), "/v1/static-gtfs-sources/1?"+query, nil), "include")
		})
	}
}

func TestGetStaticGtfsSource_MalformedIDReturns404(t *testing.T) {
	router := newStaticGtfsSourcesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/static-gtfs-sources/"+id, nil))
		})
	}
}

func TestGetStaticGtfsSource_UnknownID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	wantNotFound(t, doGet(t, newStaticGtfsSourcesRouter(t, pool), "/v1/static-gtfs-sources/999999", nil))
}

func TestGetStaticGtfsSource_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://gtfs.example.com/feed.zip')"); err != nil {
		t.Fatalf("insert static_gtfs_source: %v", err)
	}

	rec := doGet(t, newStaticGtfsSourcesRouter(t, pool), "/v1/static-gtfs-sources/20", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		SourceID            int64             `json:"source_id"`
		ServerID            int64             `json:"server_id"`
		NormalizedSourceURL string            `json:"normalized_source_url"`
		Links               map[string]string `json:"links"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SourceID != 20 {
		t.Errorf("source_id = %d, want 20", body.SourceID)
	}
	if body.ServerID != 7 {
		t.Errorf("server_id = %d, want 7", body.ServerID)
	}
	if body.NormalizedSourceURL != "https://gtfs.example.com/feed.zip" {
		t.Errorf("normalized_source_url = %q, want https://gtfs.example.com/feed.zip", body.NormalizedSourceURL)
	}
	if body.Links["self"] != "/v1/static-gtfs-sources/20" {
		t.Errorf("links.self = %q", body.Links["self"])
	}
	if body.Links["server"] != "/v1/servers/7" {
		t.Errorf("links.server = %q, want /v1/servers/7", body.Links["server"])
	}
}

func TestGetStaticGtfsSource_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://gtfs.example.com/feed.zip')"); err != nil {
		t.Fatalf("insert static_gtfs_source: %v", err)
	}

	router := newStaticGtfsSourcesRouter(t, pool)
	rec := doGet(t, router, "/v1/static-gtfs-sources/20?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	serverURL := "/v1/servers/7"
	if _, ok := body.Included[serverURL]; !ok {
		t.Fatalf("included[%s] missing; included = %v", serverURL, body.Included)
	}
	get := doGet(t, router, serverURL, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", serverURL, get.Code)
	}
	if got := string(body.Included[serverURL]) + "\n"; got != get.Body.String() {
		t.Errorf("included server =\n%s\nGET returned\n%s", got, get.Body)
	}
}

func TestGetStaticGtfsSource_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://gtfs.example.com/feed.zip')"); err != nil {
		t.Fatalf("insert static_gtfs_source: %v", err)
	}

	path := "/v1/static-gtfs-sources/20"
	router := newStaticGtfsSourcesRouter(t, pool)
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
	// httptest.ResponseRecorder keeps what the handler wrote; net/http drops
	// a HEAD body on the wire. Matching headers is what this test verifies.
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
}

