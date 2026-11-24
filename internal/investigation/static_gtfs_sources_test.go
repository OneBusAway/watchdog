package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newStaticGTFSSourceRouter mounts E36 and the server detail handler directly.
// The service still has the normal server include loader registered.
func newStaticGTFSSourceRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/static-gtfs-sources/:source_id", s.getStaticGTFSSource)
	router.HEAD("/v1/static-gtfs-sources/:source_id", s.getStaticGTFSSource)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

func staticGTFSSourcePath(id int64) string {
	return "/v1/static-gtfs-sources/" + strconv.FormatInt(id, 10)
}

func TestGetStaticGTFSSource_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/static-gtfs-sources/1"); h == nil {
			t.Errorf("%s /v1/static-gtfs-sources/:source_id is not registered", method)
		}
	}
}

func TestGetStaticGTFSSource_RejectsUnknownParameter(t *testing.T) {
	wantInvalidParam(t, doGet(t, newStaticGTFSSourceRouter(t, nil), "/v1/static-gtfs-sources/1?foo=1", nil), "foo")
}

func TestGetStaticGTFSSource_MalformedIDReturns404(t *testing.T) {
	router := newStaticGTFSSourceRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/static-gtfs-sources/"+id, nil))
		})
	}
}

func TestGetStaticGTFSSource_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	wantNotFound(t, doGet(t, newStaticGTFSSourceRouter(t, pool), "/v1/static-gtfs-sources/999999", nil))
}

func TestGetStaticGTFSSource_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://example.com/gtfs.zip')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	rec := doGet(t, newStaticGTFSSourceRouter(t, pool), staticGTFSSourcePath(20), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	want := `{"source_id":20,"server_id":7,"normalized_source_url":"https://example.com/gtfs.zip","links":{"self":"/v1/static-gtfs-sources/20","server":"/v1/servers/7","versions":"/v1/static-gtfs-sources/20/versions"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestGetStaticGTFSSource_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://example.com/gtfs.zip')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	rec := doGet(t, newStaticGTFSSourceRouter(t, pool), staticGTFSSourcePath(20)+"?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	serverURL := "/v1/servers/7"
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1", len(body.Included))
	}
	server := doGet(t, newStaticGTFSSourceRouter(t, pool), serverURL, nil)
	if server.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d; body %s", serverURL, server.Code, server.Body)
	}
	if got := string(body.Included[serverURL]) + "\n"; got != server.Body.String() {
		t.Errorf("included server = %s; GET server = %s", got, server.Body)
	}
}

func TestGetStaticGTFSSource_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://example.com/gtfs.zip')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	router := newStaticGTFSSourceRouter(t, pool)
	first := doGet(t, router, staticGTFSSourcePath(20), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("response has no ETag")
	}
	second := doGet(t, router, staticGTFSSourcePath(20), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetStaticGTFSSource_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://example.com/gtfs.zip')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	srv := httptest.NewServer(newStaticGTFSSourceRouter(t, pool))
	t.Cleanup(srv.Close)
	url := srv.URL + staticGTFSSourcePath(20)
	get, getBody := sendRequest(t, http.MethodGet, url)
	head, headBody := sendRequest(t, http.MethodHead, url)
	if head.StatusCode != get.StatusCode {
		t.Errorf("HEAD status = %d, GET status = %d", head.StatusCode, get.StatusCode)
	}
	get.Header.Del("Date")
	head.Header.Del("Date")
	if !reflect.DeepEqual(head.Header, get.Header) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header, get.Header)
	}
	if len(getBody) == 0 {
		t.Error("GET body is empty")
	}
	if len(headBody) != 0 {
		t.Errorf("HEAD body = %q, want none", headBody)
	}
}

func TestGetStaticGTFSSource_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (20, 7, 'https://example.com/gtfs.zip')"); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	router := newStaticGTFSSourceRouter(t, pool)
	for _, tc := range []struct {
		name, query, wantCache string
	}{
		{name: "without include", wantCache: "private, max-age=86400"},
		{name: "with server include", query: "?include=server", wantCache: "private, max-age=30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, router, staticGTFSSourcePath(20)+tc.query, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
			if got := rec.Header().Get("Last-Modified"); got != "" {
				t.Errorf("Last-Modified = %q, want none", got)
			}
		})
	}
}
