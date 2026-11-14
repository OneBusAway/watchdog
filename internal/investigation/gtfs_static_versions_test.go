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
	"watchdog.onebusaway.org/internal/models"
)

// newGTFSStaticVersionsRouter returns a router serving getGTFSStaticVersion
// and getServer (for included server checks), on a service using pool.
// Mounts without the pipeline, bypassing authenticate until #197.
func newGTFSStaticVersionsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return nil }

	router := httprouter.New()
	router.GET("/v1/gtfs-static-versions/:gtfs_static_version_id", s.getGTFSStaticVersion)
	router.HEAD("/v1/gtfs-static-versions/:gtfs_static_version_id", s.getGTFSStaticVersion)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

// insertStaticVersion records a gtfs_static_version row and returns its ID.
func insertStaticVersion(t *testing.T, pool *pgxpool.Pool, serverID int64, publishedAt time.Time) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		serverID, publishedAt)
}

// insertStaticSource records a static_gtfs_source and returns its ID.
func insertStaticSource(t *testing.T, pool *pgxpool.Pool, serverID int64, normalizedURL string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO static_gtfs_source (server_id, normalized_source_url) VALUES ($1, $2) RETURNING source_id",
		serverID, normalizedURL)
}

// addVersionSource links a source to a version with the given sha256.
func addVersionSource(t *testing.T, pool *pgxpool.Pool, versionID, sourceID int64, sha256 string) {
	t.Helper()
	if err := dbtest.Exec(pool,
		"INSERT INTO gtfs_static_version_source (gtfs_static_version_id, source_id, source_content_sha256) VALUES ($1, $2, $3)",
		versionID, sourceID, sha256); err != nil {
		t.Fatalf("add version source: %v", err)
	}
}

// addVersionAgency links an agency to a version.
func addVersionAgency(t *testing.T, pool *pgxpool.Pool, versionID, agencyID int64) {
	t.Helper()
	if err := dbtest.Exec(pool,
		"INSERT INTO gtfs_static_version_agency (gtfs_static_version_id, agency_id) VALUES ($1, $2)",
		versionID, agencyID); err != nil {
		t.Fatalf("add version agency: %v", err)
	}
}

func staticVersionPath(id int64) string {
	return "/v1/gtfs-static-versions/" + strconv.FormatInt(id, 10)
}

func TestGetGTFSStaticVersion_Registered(t *testing.T) {
	s, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/gtfs-static-versions/1"); h == nil {
			t.Errorf("%s /v1/gtfs-static-versions/:gtfs_static_version_id is not registered", method)
		}
	}
	if _, ok := s.loaders[kindGTFSStaticVersion]; !ok {
		t.Error("no loader registered for gtfs_static_version")
	}
}

func TestGetGTFSStaticVersion_RejectsUnknownParameter(t *testing.T) {
	router := newGTFSStaticVersionsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/gtfs-static-versions/1?foo=bar", nil), "foo")
}

func TestGetGTFSStaticVersion_RejectsBadInclude(t *testing.T) {
	router := newGTFSStaticVersionsRouter(t, nil)
	for _, query := range []string{"include=self", "include=sources", "include=", "include=server,server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/gtfs-static-versions/1?"+query, nil), "include")
		})
	}
}

func TestGetGTFSStaticVersion_MalformedIDReturns404(t *testing.T) {
	router := newGTFSStaticVersionsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/gtfs-static-versions/"+id, nil))
		})
	}
}

func TestGetGTFSStaticVersion_UnknownIDReturns404(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	router := newGTFSStaticVersionsRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/gtfs-static-versions/999999", nil))
}

func TestGetGTFSStaticVersion_ReturnsRepresentation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)

	serverID := insertServer(t, pool, "https://example.com", "Example")
	publishedAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	versionID := insertStaticVersion(t, pool, serverID, publishedAt)
	router := newGTFSStaticVersionsRouter(t, pool)

	rec := doGet(t, router, staticVersionPath(versionID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		GtfsStaticVersionID   int64             `json:"gtfs_static_version_id"`
		ServerID              int64             `json:"server_id"`
		PublishedAt           string            `json:"published_at"`
		CombinedContentSha256 *string           `json:"combined_content_sha256"`
		ParserVersion         *string           `json:"parser_version"`
		Sources               []json.RawMessage `json:"sources"`
		Agencies              []json.RawMessage `json:"agencies"`
		Links                 map[string]string `json:"links"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.GtfsStaticVersionID != versionID {
		t.Errorf("gtfs_static_version_id = %d, want %d", body.GtfsStaticVersionID, versionID)
	}
	if body.ServerID != serverID {
		t.Errorf("server_id = %d, want %d", body.ServerID, serverID)
	}
	if body.PublishedAt != "2026-10-24T12:00:00Z" {
		t.Errorf("published_at = %q, want 2026-10-24T12:00:00Z", body.PublishedAt)
	}
	if body.CombinedContentSha256 != nil {
		t.Errorf("combined_content_sha256 = %v, want null", body.CombinedContentSha256)
	}
	if len(body.Sources) != 0 {
		t.Errorf("sources = %v, want []", body.Sources)
	}
	if len(body.Agencies) != 0 {
		t.Errorf("agencies = %v, want []", body.Agencies)
	}
	wantSelf := staticVersionPath(versionID)
	if got := body.Links["self"]; got != wantSelf {
		t.Errorf("links.self = %q, want %q", got, wantSelf)
	}
	wantServer := "/v1/servers/" + strconv.FormatInt(serverID, 10)
	if got := body.Links["server"]; got != wantServer {
		t.Errorf("links.server = %q, want %q", got, wantServer)
	}
}

func TestGetGTFSStaticVersion_SourcesAndAgencies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)

	serverID := insertServer(t, pool, "https://example.com", "Example")
	versionID := insertStaticVersion(t, pool, serverID, time.Now())

	// Insert sources out of ID order so a missing ORDER BY shows.
	sha256a := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha256b := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	srcB := insertStaticSource(t, pool, serverID, "https://feeds.example.com/b")
	srcA := insertStaticSource(t, pool, serverID, "https://feeds.example.com/a")
	addVersionSource(t, pool, versionID, srcB, sha256b)
	addVersionSource(t, pool, versionID, srcA, sha256a)

	agencyB := insertAgency(t, pool, serverID, "40", nil)
	agencyA := insertAgency(t, pool, serverID, "1", nil)
	addVersionAgency(t, pool, versionID, agencyB)
	addVersionAgency(t, pool, versionID, agencyA)

	router := newGTFSStaticVersionsRouter(t, pool)
	rec := doGet(t, router, staticVersionPath(versionID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var body struct {
		Sources []struct {
			SourceID            int64  `json:"source_id"`
			NormalizedSourceURL string `json:"normalized_source_url"`
			SourceContentSha256 string `json:"source_content_sha256"`
		} `json:"sources"`
		Agencies []struct {
			AgencyID         int64  `json:"agency_id"`
			ExternalAgencyID string `json:"external_agency_id"`
		} `json:"agencies"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// srcB was inserted first so has the lower source_id; ORDER BY source_id
	// ascending means srcB comes first, then srcA.
	if len(body.Sources) != 2 {
		t.Fatalf("len(sources) = %d, want 2", len(body.Sources))
	}
	if body.Sources[0].SourceID != srcB {
		t.Errorf("sources[0].source_id = %d, want %d", body.Sources[0].SourceID, srcB)
	}
	if body.Sources[0].NormalizedSourceURL != "https://feeds.example.com/b" {
		t.Errorf("sources[0].normalized_source_url = %q, want https://feeds.example.com/b", body.Sources[0].NormalizedSourceURL)
	}
	if body.Sources[0].SourceContentSha256 != sha256b {
		t.Errorf("sources[0].source_content_sha256 mismatch")
	}
	if body.Sources[1].SourceID != srcA {
		t.Errorf("sources[1].source_id = %d, want %d", body.Sources[1].SourceID, srcA)
	}

	// agencyB was inserted first so has the lower agency_id; ORDER BY agency_id
	// ascending means agencyB comes first, then agencyA.
	if len(body.Agencies) != 2 {
		t.Fatalf("len(agencies) = %d, want 2", len(body.Agencies))
	}
	if body.Agencies[0].AgencyID != agencyB {
		t.Errorf("agencies[0].agency_id = %d, want %d", body.Agencies[0].AgencyID, agencyB)
	}
	if body.Agencies[0].ExternalAgencyID != "40" {
		t.Errorf("agencies[0].external_agency_id = %q, want 40", body.Agencies[0].ExternalAgencyID)
	}
	if body.Agencies[1].AgencyID != agencyA {
		t.Errorf("agencies[1].agency_id = %d, want %d", body.Agencies[1].AgencyID, agencyA)
	}
}

func TestGetGTFSStaticVersion_ETag(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	versionID := insertStaticVersion(t, pool, serverID, time.Now())
	router := newGTFSStaticVersionsRouter(t, pool)

	first := doGet(t, router, staticVersionPath(versionID), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	second := doGet(t, router, staticVersionPath(versionID), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
}

func TestGetGTFSStaticVersion_Head(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	versionID := insertStaticVersion(t, pool, serverID, time.Now())
	router := newGTFSStaticVersionsRouter(t, pool)
	path := staticVersionPath(versionID)

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

func TestGetGTFSStaticVersion_IncludeServer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	versionID := insertStaticVersion(t, pool, serverID, time.Now())
	router := newGTFSStaticVersionsRouter(t, pool)

	rec := doGet(t, router, staticVersionPath(versionID)+"?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
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
	if testing.Short() {
		t.Skip("requires database")
	}
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")

	sha256 := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	srcID := insertStaticSource(t, pool, serverID, "https://feeds.example.com/gtfs.zip")
	agencyID := insertAgency(t, pool, serverID, "1", nil)

	ids := make([]int64, 3)
	for i := range ids {
		ids[i] = insertStaticVersion(t, pool, serverID, time.Now())
	}
	// Add source and agency to the first version only.
	addVersionSource(t, pool, ids[0], srcID, sha256)
	addVersionAgency(t, pool, ids[0], agencyID)

	// Extra version not in the batch — loader must not return it.
	insertStaticVersion(t, pool, serverID, time.Now())

	router := newGTFSStaticVersionsRouter(t, pool)

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindGTFSStaticVersion](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 3 queries: versions, sources, agencies.
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
		rec := doGet(t, router, staticVersionPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET version %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("version %d: loaded\n%s\nGET returned\n%s", id, data, rec.Body)
		}
	}
}
