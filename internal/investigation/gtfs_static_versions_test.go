package investigation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newGTFSStaticVersionsRouter mounts E35, and E03 so an included server can be
// compared with its GET body, on a service using pool (nil for tests that
// never reach the database). The service's loaders are registered, so include
// works.
func newGTFSStaticVersionsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/gtfs-static-versions/:gtfs_static_version_id", s.getGTFSStaticVersion)
	router.HEAD("/v1/gtfs-static-versions/:gtfs_static_version_id", s.getGTFSStaticVersion)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

func gtfsStaticVersionPath(id int64) string {
	return "/v1/gtfs-static-versions/" + strconv.FormatInt(id, 10)
}

// staticPublished is when a test version is published unless the test says
// otherwise.
var staticPublished = time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)

// sha256Hex is a well-formed content hash: 64 copies of the hex digit c.
func sha256Hex(c string) string { return strings.Repeat(c, 64) }

// insertStaticVersion records a static version of serverID and returns its ID.
// A nil hash or parser version is stored as NULL.
func insertStaticVersion(t *testing.T, pool *pgxpool.Pool, serverID int64, published time.Time, hash, parser *string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO gtfs_static_version (server_id, published_at, combined_content_sha256, parser_version)
		 VALUES ($1, $2, $3, $4) RETURNING gtfs_static_version_id`,
		serverID, published, hash, parser)
}

// insertStaticSource records a static GTFS source URL of serverID and returns
// its ID.
func insertStaticSource(t *testing.T, pool *pgxpool.Pool, serverID int64, sourceURL string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO static_gtfs_source (server_id, normalized_source_url) VALUES ($1, $2) RETURNING source_id",
		serverID, sourceURL)
}

// addVersionSource records that versionID was built from sourceID, whose
// content had the given hash in that version.
func addVersionSource(t *testing.T, pool *pgxpool.Pool, versionID, sourceID int64, hash string) {
	t.Helper()
	if err := dbtest.Exec(pool,
		"INSERT INTO gtfs_static_version_source (gtfs_static_version_id, source_id, source_content_sha256) VALUES ($1, $2, $3)",
		versionID, sourceID, hash); err != nil {
		t.Fatalf("add source %d to version %d: %v", sourceID, versionID, err)
	}
}

// addVersionAgency records that versionID covered agencyID.
func addVersionAgency(t *testing.T, pool *pgxpool.Pool, versionID, agencyID int64) {
	t.Helper()
	if err := dbtest.Exec(pool,
		"INSERT INTO gtfs_static_version_agency (gtfs_static_version_id, agency_id) VALUES ($1, $2)",
		versionID, agencyID); err != nil {
		t.Fatalf("add agency %d to version %d: %v", agencyID, versionID, err)
	}
}

// mustExec runs one statement with its arguments and fails the test if it
// fails.
func mustExec(t *testing.T, pool *pgxpool.Pool, stmt string, args ...any) {
	t.Helper()
	if err := dbtest.Exec(pool, stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func TestGetGTFSStaticVersion_Registered(t *testing.T) {
	s, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/gtfs-static-versions/1"); h == nil {
			t.Errorf("%s /v1/gtfs-static-versions/:gtfs_static_version_id is not registered", method)
		}
	}
	if _, ok := s.loaders[kindGTFSStaticVersion]; !ok {
		t.Error("no loader registered for static versions")
	}
}

func TestGetGTFSStaticVersion_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	mustExec(t, pool, "INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')")
	mustExec(t, pool, "INSERT INTO agency (agency_id, server_id, external_agency_id) OVERRIDING SYSTEM VALUE VALUES (40, 7, '1')")
	mustExec(t, pool, "INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE VALUES (30, 7, 'https://example.com/gtfs.zip')")
	// Published with a +02:00 offset, so the response must convert it to UTC.
	mustExec(t, pool,
		`INSERT INTO gtfs_static_version (gtfs_static_version_id, server_id, published_at, combined_content_sha256, parser_version)
		 OVERRIDING SYSTEM VALUE VALUES (12, 7, '2026-10-01T04:00:00.123456+02:00', $1, 'watchdog/1.4.0')`,
		sha256Hex("a"))
	addVersionSource(t, pool, 12, 30, sha256Hex("b"))
	addVersionAgency(t, pool, 12, 40)
	// Version 13 has no hash, no parser version, no sources and no agencies.
	mustExec(t, pool, "INSERT INTO gtfs_static_version (gtfs_static_version_id, server_id, published_at) OVERRIDING SYSTEM VALUE VALUES (13, 7, '2026-10-02T00:00:00Z')")
	router := newGTFSStaticVersionsRouter(t, pool)

	for _, tc := range []struct {
		path, want string
	}{
		{"/v1/gtfs-static-versions/12", `{"gtfs_static_version_id":12,"server_id":7,"published_at":"2026-10-01T02:00:00.123456Z",` +
			`"combined_content_sha256":"` + sha256Hex("a") + `","parser_version":"watchdog/1.4.0",` +
			`"sources":[{"source_id":30,"normalized_source_url":"https://example.com/gtfs.zip","source_content_sha256":"` + sha256Hex("b") + `"}],` +
			`"agencies":[{"agency_id":40,"external_agency_id":"1"}],` +
			`"links":{"self":"/v1/gtfs-static-versions/12","server":"/v1/servers/7"}}` + "\n"},
		{"/v1/gtfs-static-versions/13", `{"gtfs_static_version_id":13,"server_id":7,"published_at":"2026-10-02T00:00:00Z",` +
			`"combined_content_sha256":null,"parser_version":null,"sources":[],"agencies":[],` +
			`"links":{"self":"/v1/gtfs-static-versions/13","server":"/v1/servers/7"}}` + "\n"},
	} {
		rec := doGet(t, router, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type = %q, want application/json", tc.path, got)
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s: body =\n%s\nwant\n%s", tc.path, got, tc.want)
		}
	}
}

func TestGetGTFSStaticVersion_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertStaticVersion(t, pool, insertServer(t, pool, "https://example.com", "Example"), staticPublished, nil, nil)
	router := newGTFSStaticVersionsRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/gtfs-static-versions/999999", nil))
}

func TestGetGTFSStaticVersion_MalformedIDReturns404(t *testing.T) {
	// No database: a malformed ID must be rejected before any query.
	router := newGTFSStaticVersionsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/gtfs-static-versions/"+id, nil))
		})
	}
}

func TestGetGTFSStaticVersion_RejectsUnknownParameter(t *testing.T) {
	router := newGTFSStaticVersionsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/gtfs-static-versions/1?foo=1", nil), "foo")
}

func TestGetGTFSStaticVersion_RejectsBadInclude(t *testing.T) {
	router := newGTFSStaticVersionsRouter(t, nil)
	for _, query := range []string{"include=self", "include=sources", "include=agencies", "include=", "include=server,server", "include=server,", "include=server&include=server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/gtfs-static-versions/1?"+query, nil), "include")
		})
	}
}

func TestGetGTFSStaticVersion_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertStaticVersion(t, pool, insertServer(t, pool, "https://example.com", "Example"), staticPublished, nil, nil)
	router := newGTFSStaticVersionsRouter(t, pool)

	first := doGet(t, router, gtfsStaticVersionPath(id), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	// A published version never changes, so it is cached for a day and its
	// own publication time is its Last-Modified.
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	if got, want := first.Header().Get("Last-Modified"), staticPublished.Format(http.TimeFormat); got != want {
		t.Errorf("Last-Modified = %q, want %q", got, want)
	}

	second := doGet(t, router, gtfsStaticVersionPath(id), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

// sendRequest sends a request to a real server and returns the response with
// its body read.
func sendRequest(t *testing.T, method, url string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", method, err)
	}
	return resp, body
}

func TestGetGTFSStaticVersion_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertStaticVersion(t, pool, insertServer(t, pool, "https://example.com", "Example"), staticPublished, nil, nil)
	// A real server, so the test sees what a client sees: net/http sends a HEAD
	// response's headers and drops its body.
	srv := httptest.NewServer(newGTFSStaticVersionsRouter(t, pool))
	t.Cleanup(srv.Close)
	url := srv.URL + gtfsStaticVersionPath(id)

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

func TestGetGTFSStaticVersion_SourcesAndAgencies(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	// Rows are created and linked in descending ID order, so a missing ORDER BY
	// shows.
	mustExec(t, pool,
		"INSERT INTO agency (agency_id, server_id, external_agency_id) OVERRIDING SYSTEM VALUE VALUES (41, $1, 'b'), (40, $1, 'a')",
		serverID)
	mustExec(t, pool,
		`INSERT INTO static_gtfs_source (source_id, server_id, normalized_source_url) OVERRIDING SYSTEM VALUE
		 VALUES (31, $1, 'https://example.com/b.zip'), (30, $1, 'https://example.com/a.zip')`,
		serverID)
	version := insertStaticVersion(t, pool, serverID, staticPublished, nil, nil)
	addVersionSource(t, pool, version, 31, sha256Hex("2"))
	addVersionSource(t, pool, version, 30, sha256Hex("1"))
	addVersionAgency(t, pool, version, 41)
	addVersionAgency(t, pool, version, 40)
	// A later version built from one of the same sources, whose content
	// changed, and covering one agency. Its rows must not leak into the first.
	later := insertStaticVersion(t, pool, serverID, staticPublished.Add(time.Hour), nil, nil)
	addVersionSource(t, pool, later, 30, sha256Hex("3"))
	addVersionAgency(t, pool, later, 40)
	router := newGTFSStaticVersionsRouter(t, pool)

	for _, tc := range []struct {
		version           int64
		sources, agencies []any
	}{
		{
			version: version,
			sources: []any{
				map[string]any{"source_id": float64(30), "normalized_source_url": "https://example.com/a.zip", "source_content_sha256": sha256Hex("1")},
				map[string]any{"source_id": float64(31), "normalized_source_url": "https://example.com/b.zip", "source_content_sha256": sha256Hex("2")},
			},
			agencies: []any{
				map[string]any{"agency_id": float64(40), "external_agency_id": "a"},
				map[string]any{"agency_id": float64(41), "external_agency_id": "b"},
			},
		},
		{
			version: later,
			sources: []any{
				map[string]any{"source_id": float64(30), "normalized_source_url": "https://example.com/a.zip", "source_content_sha256": sha256Hex("3")},
			},
			agencies: []any{
				map[string]any{"agency_id": float64(40), "external_agency_id": "a"},
			},
		},
	} {
		body := decodeEpisodeBody(t, doGet(t, router, gtfsStaticVersionPath(tc.version), nil))
		if !reflect.DeepEqual(body["sources"], tc.sources) {
			t.Errorf("version %d: sources = %v, want %v", tc.version, body["sources"], tc.sources)
		}
		if !reflect.DeepEqual(body["agencies"], tc.agencies) {
			t.Errorf("version %d: agencies = %v, want %v", tc.version, body["agencies"], tc.agencies)
		}
	}
}

// TestGetGTFSStaticVersion_IncludeServer checks include=server adds the
// version's server, exactly as GET /v1/servers/{server_id} returns it, and
// that the response is then short-cached: an included server can change.
func TestGetGTFSStaticVersion_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	id := insertStaticVersion(t, pool, serverID, staticPublished, nil, nil)
	router := newGTFSStaticVersionsRouter(t, pool)

	rec := doGet(t, router, gtfsStaticVersionPath(id)+"?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if got := rec.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none", got)
	}
	var body struct {
		GTFSStaticVersionID int64                      `json:"gtfs_static_version_id"`
		Included            map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.GTFSStaticVersionID != id {
		t.Errorf("gtfs_static_version_id = %d, want %d", body.GTFSStaticVersionID, id)
	}
	serverURL := "/v1/servers/" + strconv.FormatInt(serverID, 10)
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(body.Included), body.Included)
	}
	get := doGet(t, router, serverURL, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", serverURL, get.Code)
	}
	if got := string(body.Included[serverURL]) + "\n"; got != get.Body.String() {
		t.Errorf("included server =\n%s\nGET returned\n%s", got, get.Body)
	}
}

func TestGTFSStaticVersionLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	a := insertServer(t, pool, "https://a.example.com", "A")
	b := insertServer(t, pool, "https://b.example.com", "B")
	full := insertStaticVersion(t, pool, a, staticPublished, strPtr(sha256Hex("a")), strPtr("watchdog/1.4.0"))
	addVersionSource(t, pool, full, insertStaticSource(t, pool, a, "https://a.example.com/gtfs.zip"), sha256Hex("b"))
	addVersionAgency(t, pool, full, insertAgency(t, pool, a, "1", nil))
	ids := []int64{
		full,
		insertStaticVersion(t, pool, a, staticPublished.Add(time.Hour), nil, nil), // no sources or agencies
		insertStaticVersion(t, pool, b, staticPublished, nil, nil),                // another server
	}
	insertStaticVersion(t, pool, a, staticPublished.Add(2*time.Hour), nil, nil) // not asked for
	router := newGTFSStaticVersionsRouter(t, pool)

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	// An ID with no row is left out: a link exists only when its target does.
	got, err := s.loaders[kindGTFSStaticVersion](context.Background(), dbgen.New(db), append(ids, 999999))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// One query each for the versions, their sources and their agencies,
	// whatever the number of IDs: the loader never queries once per version.
	if db.queries != 3 {
		t.Errorf("loader ran %d queries, want 3", db.queries)
	}
	if len(got) != len(ids) {
		t.Errorf("loader returned %d versions, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		data, err := encodeJSON(got[id])
		if err != nil {
			t.Fatalf("encode version %d: %v", id, err)
		}
		rec := doGet(t, router, gtfsStaticVersionPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET version %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("version %d: loaded\n%s\nGET returned\n%s", id, data, rec.Body)
		}
	}
}

// TestGTFSStaticVersion_IncludedFromTripStaticContexts checks that the loader
// turns on include=gtfs_static_version where E19 links to a version, and that
// the included version is exactly E35's body.
func TestGTFSStaticVersion_IncludedFromTripStaticContexts(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := tripStaticFixture(t, pool, "trip-1")
	episodeID := tr.episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	versionID, _ := tr.linkStaticWithRoute(t, episodeID, episodesNow.Add(-3*time.Hour), true, strPtr("route-1"))
	contexts := newTripStaticRouter(t, pool)
	versions := newGTFSStaticVersionsRouter(t, pool)

	rec := doGet(t, contexts, tripStaticContextsPath(episodeID)+"?include=gtfs_static_version", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	versionURL := gtfsStaticVersionPath(versionID)
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(body.Included), body.Included)
	}
	get := doGet(t, versions, versionURL, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", versionURL, get.Code)
	}
	if got := string(body.Included[versionURL]) + "\n"; got != get.Body.String() {
		t.Errorf("included version =\n%s\nGET returned\n%s", got, get.Body)
	}
}
