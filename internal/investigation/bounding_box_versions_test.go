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

// newBoundingBoxRouter returns a router serving getBoundingBoxVersion and
// getServer (for included comparisons) on a service using pool.
func newBoundingBoxRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool

	router := httprouter.New()
	router.GET("/v1/bounding-box-versions/:bounding_box_version_id", s.getBoundingBoxVersion)
	router.HEAD("/v1/bounding-box-versions/:bounding_box_version_id", s.getBoundingBoxVersion)
	router.GET("/v1/servers/:server_id", s.getServer)
	router.GET("/v1/agencies/:agency_id", s.getAgency)
	return router
}

// insertGtfsStaticVersion inserts a gtfs_static_version row and returns its ID.
func insertGtfsStaticVersion(t *testing.T, pool *pgxpool.Pool, serverID int64) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		serverID, time.Now())
}

// insertBoundingBoxVersion inserts a bounding_box_version row and returns its
// ID. agencyID may be nil for a server-wide box.
func insertBoundingBoxVersion(t *testing.T, pool *pgxpool.Pool, staticVersionID, serverID int64, agencyID *int64) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO bounding_box_version
		    (gtfs_static_version_id, server_id, agency_id,
		     minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		 VALUES ($1, $2, $3, 47.0, 48.0, -122.5, -121.5)
		 RETURNING bounding_box_version_id`,
		staticVersionID, serverID, agencyID)
}

func bbPath(id int64) string {
	return "/v1/bounding-box-versions/" + strconv.FormatInt(id, 10)
}

func TestGetBoundingBoxVersion_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/bounding-box-versions/1"); h == nil {
			t.Errorf("%s /v1/bounding-box-versions/:bounding_box_version_id is not registered", method)
		}
	}
}

func TestGetBoundingBoxVersion_MalformedIDReturns404(t *testing.T) {
	router := newBoundingBoxRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/bounding-box-versions/"+id, nil))
		})
	}
}

func TestGetBoundingBoxVersion_RejectsUnknownParameter(t *testing.T) {
	router := newBoundingBoxRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/bounding-box-versions/1?foo=1", nil), "foo")
}

func TestGetBoundingBoxVersion_RejectsBadInclude(t *testing.T) {
	router := newBoundingBoxRouter(t, nil)
	for _, query := range []string{"include=self", "include=", "include=server,server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/bounding-box-versions/1?"+query, nil), "include")
		})
	}
}

func TestGetBoundingBoxVersion_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newBoundingBoxRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/bounding-box-versions/999999", nil))
}

func TestGetBoundingBoxVersion_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	bbvID := insertBoundingBoxVersion(t, pool, svID, serverID, nil)
	router := newBoundingBoxRouter(t, pool)
	path := bbPath(bbvID)

	rec := doGet(t, router, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body boundingBoxVersion
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.BoundingBoxVersionID != bbvID {
		t.Errorf("bounding_box_version_id = %d, want %d", body.BoundingBoxVersionID, bbvID)
	}
	if body.ServerID != serverID {
		t.Errorf("server_id = %d, want %d", body.ServerID, serverID)
	}
	if body.GtfsStaticVersionID != svID {
		t.Errorf("gtfs_static_version_id = %d, want %d", body.GtfsStaticVersionID, svID)
	}
	if body.AgencyID != nil {
		t.Errorf("agency_id = %v, want nil", body.AgencyID)
	}
	if body.MinimumLatitude != 47.0 {
		t.Errorf("minimum_latitude = %f, want 47.0", body.MinimumLatitude)
	}
	if body.Links["self"] != path {
		t.Errorf("self link = %q, want %q", body.Links["self"], path)
	}
	if body.Links["server"] != "/v1/servers/"+strconv.FormatInt(serverID, 10) {
		t.Errorf("server link = %q", body.Links["server"])
	}
	if body.Links["gtfs_static_version"] != "/v1/gtfs-static-versions/"+strconv.FormatInt(svID, 10) {
		t.Errorf("gtfs_static_version link = %q", body.Links["gtfs_static_version"])
	}
	// No agency link for a server-wide box.
	if _, ok := body.Links["agency"]; ok {
		t.Error("agency link present for server-wide bounding box")
	}
	// Immutable cache.
	if got := rec.Header().Get("Cache-Control"); got == "private, max-age=30" {
		t.Error("bounding-box-version should use immutable cache, not short cache")
	}
}

func TestGetBoundingBoxVersion_AgencySpecificBox(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	name := "Metro"
	agencyID := insertAgency(t, pool, serverID, "metro", &name)
	bbvID := insertBoundingBoxVersion(t, pool, svID, serverID, &agencyID)
	router := newBoundingBoxRouter(t, pool)

	rec := doGet(t, router, bbPath(bbvID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body boundingBoxVersion
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AgencyID == nil || *body.AgencyID != agencyID {
		t.Errorf("agency_id = %v, want %d", body.AgencyID, agencyID)
	}
	if body.Links["agency"] != "/v1/agencies/"+strconv.FormatInt(agencyID, 10) {
		t.Errorf("agency link = %q", body.Links["agency"])
	}
}

func TestGetBoundingBoxVersion_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	bbvID := insertBoundingBoxVersion(t, pool, svID, serverID, nil)
	router := newBoundingBoxRouter(t, pool)
	path := bbPath(bbvID)

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

func TestGetBoundingBoxVersion_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	bbvID := insertBoundingBoxVersion(t, pool, svID, serverID, nil)
	router := newBoundingBoxRouter(t, pool)
	path := bbPath(bbvID)

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

func TestGetBoundingBoxVersion_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	bbvID := insertBoundingBoxVersion(t, pool, svID, serverID, nil)
	router := newBoundingBoxRouter(t, pool)
	path := bbPath(bbvID)
	serverURL := "/v1/servers/" + strconv.FormatInt(serverID, 10)

	rec := doGet(t, router, path+"?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body struct {
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
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

func TestBoundingBoxVersionLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	svID := insertGtfsStaticVersion(t, pool, serverID)
	ids := []int64{
		insertBoundingBoxVersion(t, pool, svID, serverID, nil),
	}
	// Use separate static versions to satisfy the unique constraint
	// (one server-wide box per static version).
	svID2 := insertGtfsStaticVersion(t, pool, serverID)
	ids = append(ids, insertBoundingBoxVersion(t, pool, svID2, serverID, nil))
	svID3 := insertGtfsStaticVersion(t, pool, serverID)
	insertBoundingBoxVersion(t, pool, svID3, serverID, nil) // not asked

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindBoundingBoxVersion](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("loadBoundingBoxVersions: %v", err)
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
