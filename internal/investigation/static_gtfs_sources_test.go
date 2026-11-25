package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/middleware"
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

func newStaticGTFSSourceVersionsFixture(t *testing.T, hashes ...string) (http.Handler, string, []int64) {
	t.Helper()
	pool, router := newStaticVersionListTest(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	sourceID := insertStaticSource(t, pool, serverID, "https://example.com/feed.zip")
	ids := make([]int64, 0, len(hashes))
	for _, hash := range hashes {
		id := insertStaticVersion(t, pool, serverID, staticPublished, nil, nil)
		addVersionSource(t, pool, id, sourceID, hash)
		ids = append(ids, id)
	}
	return router, staticGTFSSourcePath(sourceID) + "/versions", ids
}

type staticGTFSSourceVersionsResponse struct {
	Items []struct {
		ID          int64  `json:"gtfs_static_version_id"`
		PublishedAt string `json:"published_at"`
		Hash        string `json:"source_content_sha256"`
		Links       links  `json:"links"`
	} `json:"items"`
	Links    links                      `json:"links"`
	Included map[string]json.RawMessage `json:"included"`
}

func getStaticGTFSSourceVersionsPage(t *testing.T, router http.Handler, path string, wantIDs ...int64) staticGTFSSourceVersionsResponse {
	t.Helper()
	rec := doGet(t, router, path, staticVersionListHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200; body %s", path, rec.Code, rec.Body)
	}
	var body staticGTFSSourceVersionsResponse
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("decode source versions: %v", err)
	}
	if body.Items == nil {
		t.Fatal("items must be a JSON array")
	}
	ids := make([]int64, 0, len(body.Items))
	for _, item := range body.Items {
		ids = append(ids, item.ID)
		want := links{"gtfs_static_version": gtfsStaticVersionPath(item.ID)}
		if !reflect.DeepEqual(item.Links, want) {
			t.Errorf("version %d links = %v, want %v", item.ID, item.Links, want)
		}
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("GET %s: version IDs = %v, want %v", path, ids, wantIDs)
	}
	return body
}

func TestListStaticGTFSSourceVersions_ReturnsNewestFirst(t *testing.T) {
	pool, router := newStaticVersionListTest(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	sourceID := insertStaticSource(t, pool, serverID, "https://example.com/feed.zip")
	otherSourceID := insertStaticSource(t, pool, serverID, "https://example.com/other.zip")
	first := insertStaticVersion(t, pool, serverID, staticPublished, nil, nil)
	second := insertStaticVersion(t, pool, serverID, staticPublished.Add(time.Hour), nil, nil)
	third := insertStaticVersion(t, pool, serverID, staticPublished.Add(-time.Hour), nil, nil)
	other := insertStaticVersion(t, pool, serverID, staticPublished, nil, nil)
	for _, id := range []int64{first, second, third} {
		addVersionSource(t, pool, id, sourceID, sha256Hex("a"))
	}
	addVersionSource(t, pool, other, otherSourceID, sha256Hex("b"))
	getStaticGTFSSourceVersionsPage(t, router, staticGTFSSourcePath(sourceID)+"/versions", third, second, first)
}

func TestListStaticGTFSSourceVersions_Paginates(t *testing.T) {
	hash := sha256Hex("a")
	router, path, ids := newStaticGTFSSourceVersionsFixture(t, hash, hash, hash)
	firstPath := path + "?page_size=2"
	first := getStaticGTFSSourceVersionsPage(t, router, firstPath, ids[2], ids[1])
	if first.Links["self"] != firstPath {
		t.Errorf("self link = %q, want %q", first.Links["self"], firstPath)
	}
	next := first.Links["next"]
	if next == "" {
		t.Fatal("first page has no next link")
	}
	last := getStaticGTFSSourceVersionsPage(t, router, next, ids[0])
	wantLinks := links{"self": next}
	if !reflect.DeepEqual(last.Links, wantLinks) {
		t.Errorf("last page links = %v, want %v", last.Links, wantLinks)
	}
}

func TestListStaticGTFSSourceVersions_EmptyResult(t *testing.T) {
	router, path, _ := newStaticGTFSSourceVersionsFixture(t)
	for _, query := range []string{"", "?include=gtfs_static_version"} {
		requestPath := path + query
		body := getStaticGTFSSourceVersionsPage(t, router, requestPath)
		wantLinks := links{"self": requestPath}
		if !reflect.DeepEqual(body.Links, wantLinks) {
			t.Errorf("GET %s: links = %v, want %v", requestPath, body.Links, wantLinks)
		}
		if (body.Included != nil) != (query != "") || len(body.Included) != 0 {
			t.Errorf("GET %s: included = %#v", requestPath, body.Included)
		}
	}
}

func TestListStaticGTFSSourceVersions_RejectsBadPageSize(t *testing.T) {
	_, router := newStaticVersionListTest(t)
	for _, query := range []string{"page_size=0", "page_size=201", "page_size=1&page_size=2"} {
		t.Run(query, func(t *testing.T) {
			rec := doGet(t, router, staticGTFSSourcePath(1)+"/versions?"+query, staticVersionListHeaders())
			wantInvalidParam(t, rec, "page_size")
		})
	}
}

func TestListStaticGTFSSourceVersions_RejectsBadCursor(t *testing.T) {
	_, router := newStaticVersionListTest(t)
	query := url.Values{"cursor": {"%%%"}}
	rec := doGet(t, router, staticGTFSSourcePath(1)+"/versions?"+query.Encode(), staticVersionListHeaders())
	wantInvalidParam(t, rec, "cursor")
}

func TestListStaticGTFSSourceVersions_RejectsUnknownParameter(t *testing.T) {
	_, router := newStaticVersionListTest(t)
	rec := doGet(t, router, staticGTFSSourcePath(1)+"/versions?foo=1", staticVersionListHeaders())
	wantInvalidParam(t, rec, "foo")
}

func TestListStaticGTFSSourceVersions_Head(t *testing.T) {
	router, path, _ := newStaticGTFSSourceVersionsFixture(t, sha256Hex("a"))
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	get, getBody := sendRequestWithHeaders(t, http.MethodGet, srv.URL+path, staticVersionListHeaders())
	head, headBody := sendRequestWithHeaders(t, http.MethodHead, srv.URL+path, staticVersionListHeaders())
	if get.StatusCode != http.StatusOK || head.StatusCode != get.StatusCode {
		t.Errorf("GET status = %d, HEAD status = %d, want both 200", get.StatusCode, head.StatusCode)
	}
	get.Header.Del("Date")
	head.Header.Del("Date")
	if !reflect.DeepEqual(head.Header, get.Header) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header, get.Header)
	}
	if len(getBody) == 0 || len(headBody) != 0 {
		t.Errorf("GET body has %d bytes and HEAD body has %d bytes", len(getBody), len(headBody))
	}
}

func TestListStaticGTFSSourceVersions_Hashes(t *testing.T) {
	a, b := sha256Hex("a"), sha256Hex("b")
	router, path, ids := newStaticGTFSSourceVersionsFixture(t, b, a, a)
	body := getStaticGTFSSourceVersionsPage(t, router, path, ids[2], ids[1], ids[0])
	wantPublishedAt := staticPublished.UTC().Format(time.RFC3339Nano)
	for i, want := range []string{a, a, b} {
		if got := body.Items[i].Hash; got != want {
			t.Errorf("item %d hash = %q, want %q", i, got, want)
		}
		if got := body.Items[i].PublishedAt; got != wantPublishedAt {
			t.Errorf("item %d published_at = %q, want %q", i, got, wantPublishedAt)
		}
	}
}

func TestListStaticGTFSSourceVersions_UnknownSource(t *testing.T) {
	_, router := newStaticVersionListTest(t)
	for _, id := range []string{"999999", "0", "-1", "abc"} {
		t.Run(id, func(t *testing.T) {
			rec := doGet(t, router, "/v1/static-gtfs-sources/"+id+"/versions", staticVersionListHeaders())
			wantNotFound(t, rec)
		})
	}
}

func TestListStaticGTFSSourceVersions_Include(t *testing.T) {
	router, path, ids := newStaticGTFSSourceVersionsFixture(t, sha256Hex("a"), sha256Hex("b"))
	body := getStaticGTFSSourceVersionsPage(t, router, path+"?include=gtfs_static_version&page_size=1", ids[1])
	versionPath := gtfsStaticVersionPath(ids[1])
	if len(body.Included) != 1 || body.Included[versionPath] == nil {
		t.Fatalf("included = %v, want only %s", body.Included, versionPath)
	}
	version := doGet(t, router, versionPath, staticVersionListHeaders())
	if version.Code != http.StatusOK || string(body.Included[versionPath])+"\n" != version.Body.String() {
		t.Errorf("included = %s; GET %s = %d %s", body.Included[versionPath], versionPath, version.Code, version.Body)
	}
	for _, value := range []string{"server", "gtfs_static_version,", "gtfs_static_version,gtfs_static_version"} {
		t.Run(value, func(t *testing.T) {
			rec := doGet(t, router, path+"?include="+value, staticVersionListHeaders())
			wantInvalidParam(t, rec, "include")
		})
	}
}

func TestListStaticGTFSSourceVersions_Cache(t *testing.T) {
	router, path, _ := newStaticGTFSSourceVersionsFixture(t, sha256Hex("a"))
	router = middleware.SecurityHeaders(router)
	headers := staticVersionListHeaders()
	get := doGet(t, router, path, headers)
	if get.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", get.Code, get.Body)
	}
	if got := get.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	tag := get.Header().Get("ETag")
	if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Fatalf("ETag = %q, want a strong ETag", tag)
	}
	headers["If-None-Match"] = tag
	match := doGet(t, router, path, headers)
	if match.Code != http.StatusNotModified || match.Body.Len() != 0 {
		t.Errorf("conditional response = %d %q, want 304 with an empty body", match.Code, match.Body.String())
	}
}
