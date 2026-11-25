package investigation

import (
	"context"
	"encoding/json"
	"fmt"
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

// newBoundingBoxVersionsRouter mounts E29, server detail, agency detail,
// and static version detail handlers directly. The service still has all
// normal include loaders registered via newTestService.
func newBoundingBoxVersionsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/bounding-box-versions/:bounding_box_version_id", s.getBoundingBoxVersion)
	router.HEAD("/v1/bounding-box-versions/:bounding_box_version_id", s.getBoundingBoxVersion)
	router.GET("/v1/servers/:server_id", s.getServer)
	router.GET("/v1/agencies/:agency_id", s.getAgency)
	router.GET("/v1/gtfs-static-versions/:gtfs_static_version_id", s.getGTFSStaticVersion)
	return router
}

func boundingBoxVersionPath(id int64) string {
	return "/v1/bounding-box-versions/" + strconv.FormatInt(id, 10)
}

func insertBoundingBox(t *testing.T, pool *pgxpool.Pool, staticVersionID, serverID int64, agencyID *int64, minLat, maxLat, minLon, maxLon float64) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO bounding_box_version (gtfs_static_version_id, server_id, agency_id, minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING bounding_box_version_id`,
		staticVersionID, serverID, agencyID, minLat, maxLat, minLon, maxLon)
}

func TestGetBoundingBoxVersion_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/bounding-box-versions/1"); h == nil {
			t.Errorf("%s /v1/bounding-box-versions/:bounding_box_version_id is not registered", method)
		}
	}
}

func TestGetBoundingBoxVersion_RejectsUnknownParameter(t *testing.T) {
	wantInvalidParam(t, doGet(t, newBoundingBoxVersionsRouter(t, nil), "/v1/bounding-box-versions/1?foo=1", nil), "foo")
}

func TestGetBoundingBoxVersion_MalformedIDReturns404(t *testing.T) {
	router := newBoundingBoxVersionsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/bounding-box-versions/"+id, nil))
		})
	}
}

func TestGetBoundingBoxVersion_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	wantNotFound(t, doGet(t, newBoundingBoxVersionsRouter(t, pool), "/v1/bounding-box-versions/999999", nil))
}

func TestGetBoundingBoxVersion_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVersionID := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	agencyID := insertAgency(t, pool, serverID, "1", nil)
	boxID := insertBoundingBox(t, pool, staticVersionID, serverID, &agencyID, 47.0, 48.0, -123.0, -122.0)

	rec := doGet(t, newBoundingBoxVersionsRouter(t, pool), boundingBoxVersionPath(boxID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var got boundingBoxVersion
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got.BoundingBoxVersionID != boxID {
		t.Errorf("bounding_box_version_id = %d, want %d", got.BoundingBoxVersionID, boxID)
	}
	if got.GtfsStaticVersionID != staticVersionID {
		t.Errorf("gtfs_static_version_id = %d, want %d", got.GtfsStaticVersionID, staticVersionID)
	}
	if got.ServerID != serverID {
		t.Errorf("server_id = %d, want %d", got.ServerID, serverID)
	}
	if got.AgencyID == nil || *got.AgencyID != agencyID {
		t.Errorf("agency_id = %v, want %d", got.AgencyID, agencyID)
	}
	if got.Scope != "agency" {
		t.Errorf("scope = %q, want %q", got.Scope, "agency")
	}
	if got.MinimumLatitude != 47.0 || got.MaximumLatitude != 48.0 || got.MinimumLongitude != -123.0 || got.MaximumLongitude != -122.0 {
		t.Errorf("bounds = (%v, %v, %v, %v), want (47, 48, -123, -122)",
			got.MinimumLatitude, got.MaximumLatitude, got.MinimumLongitude, got.MaximumLongitude)
	}

	wantLinks := links{
		"self":                boundingBoxVersionPath(boxID),
		"gtfs_static_version": "/v1/gtfs-static-versions/" + strconv.FormatInt(staticVersionID, 10),
		"server":              "/v1/servers/" + strconv.FormatInt(serverID, 10),
		"agency":              "/v1/agencies/" + strconv.FormatInt(agencyID, 10),
	}
	if !reflect.DeepEqual(got.Links, wantLinks) {
		t.Errorf("links = %+v, want %+v", got.Links, wantLinks)
	}
}

func TestGetBoundingBoxVersion_Scope(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVer1 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	staticVer2 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), nil, nil)
	agencyID := insertAgency(t, pool, serverID, "1", nil)

	// Server-wide box: agency_id is null.
	serverBoxID := insertBoundingBox(t, pool, staticVer1, serverID, nil, 46.0, 49.0, -124.0, -121.0)
	// Agency-specific box: agency_id is not null.
	agencyBoxID := insertBoundingBox(t, pool, staticVer2, serverID, &agencyID, 47.0, 48.0, -123.0, -122.0)

	router := newBoundingBoxVersionsRouter(t, pool)

	// Check server-wide box
	recServer := doGet(t, router, boundingBoxVersionPath(serverBoxID), nil)
	if recServer.Code != http.StatusOK {
		t.Fatalf("server box status = %d, want 200", recServer.Code)
	}
	var gotServer boundingBoxVersion
	if err := json.NewDecoder(recServer.Body).Decode(&gotServer); err != nil {
		t.Fatalf("decode server box: %v", err)
	}
	if gotServer.Scope != "server" {
		t.Errorf("server box scope = %q, want %q", gotServer.Scope, "server")
	}
	if gotServer.AgencyID != nil {
		t.Errorf("server box agency_id = %v, want nil", gotServer.AgencyID)
	}
	if _, ok := gotServer.Links["agency"]; ok {
		t.Errorf("server box links has agency, want none: %+v", gotServer.Links)
	}
	if gotServer.Links["self"] != boundingBoxVersionPath(serverBoxID) {
		t.Errorf("server box self link = %q, want %q", gotServer.Links["self"], boundingBoxVersionPath(serverBoxID))
	}

	// Check agency-scoped box
	recAgency := doGet(t, router, boundingBoxVersionPath(agencyBoxID), nil)
	if recAgency.Code != http.StatusOK {
		t.Fatalf("agency box status = %d, want 200", recAgency.Code)
	}
	var gotAgency boundingBoxVersion
	if err := json.NewDecoder(recAgency.Body).Decode(&gotAgency); err != nil {
		t.Fatalf("decode agency box: %v", err)
	}
	if gotAgency.Scope != "agency" {
		t.Errorf("agency box scope = %q, want %q", gotAgency.Scope, "agency")
	}
	if gotAgency.AgencyID == nil || *gotAgency.AgencyID != agencyID {
		t.Errorf("agency box agency_id = %v, want %d", gotAgency.AgencyID, agencyID)
	}
	wantAgencyLink := "/v1/agencies/" + strconv.FormatInt(agencyID, 10)
	if gotAgency.Links["agency"] != wantAgencyLink {
		t.Errorf("agency box agency link = %q, want %q", gotAgency.Links["agency"], wantAgencyLink)
	}
}

func TestGetBoundingBoxVersion_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVer := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	boxID := insertBoundingBox(t, pool, staticVer, serverID, nil, 47.0, 48.0, -123.0, -122.0)

	router := newBoundingBoxVersionsRouter(t, pool)
	first := doGet(t, router, boundingBoxVersionPath(boxID), nil)
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
	second := doGet(t, router, boundingBoxVersionPath(boxID), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetBoundingBoxVersion_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVer := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	boxID := insertBoundingBox(t, pool, staticVer, serverID, nil, 47.0, 48.0, -123.0, -122.0)

	srv := httptest.NewServer(newBoundingBoxVersionsRouter(t, pool))
	t.Cleanup(srv.Close)
	url := srv.URL + boundingBoxVersionPath(boxID)
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

func TestGetBoundingBoxVersion_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVer := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	boxID := insertBoundingBox(t, pool, staticVer, serverID, nil, 47.0, 48.0, -123.0, -122.0)

	router := newBoundingBoxVersionsRouter(t, pool)
	for _, tc := range []struct {
		name, query, wantCache string
	}{
		{name: "without include", wantCache: "private, max-age=86400"},
		{name: "with server include", query: "?include=server", wantCache: "private, max-age=30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, router, boundingBoxVersionPath(boxID)+tc.query, nil)
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

func TestGetBoundingBoxVersion_Include(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	staticVer := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	agencyID := insertAgency(t, pool, serverID, "1", nil)
	boxID := insertBoundingBox(t, pool, staticVer, serverID, &agencyID, 47.0, 48.0, -123.0, -122.0)

	router := newBoundingBoxVersionsRouter(t, pool)
	rec := doGet(t, router, fmt.Sprintf("%s?include=server,agency,gtfs_static_version", boundingBoxVersionPath(boxID)), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var body struct {
		boundingBoxVersion
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	serverURL := "/v1/servers/" + strconv.FormatInt(serverID, 10)
	agencyURL := "/v1/agencies/" + strconv.FormatInt(agencyID, 10)
	staticVerURL := "/v1/gtfs-static-versions/" + strconv.FormatInt(staticVer, 10)

	if len(body.Included) != 3 {
		t.Fatalf("included has %d entries, want 3: %+v", len(body.Included), body.Included)
	}

	for _, u := range []string{serverURL, agencyURL, staticVerURL} {
		if _, ok := body.Included[u]; !ok {
			t.Errorf("missing included URL %q", u)
		}
		expected := doGet(t, router, u, nil)
		if expected.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d; body %s", u, expected.Code, expected.Body)
		}
		if got := string(body.Included[u]) + "\n"; got != expected.Body.String() {
			t.Errorf("included %s =\n%s\nwant\n%s", u, got, expected.Body)
		}
	}
}

func TestBoundingBoxVersionLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	v1 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	v2 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), nil, nil)
	v3 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), nil, nil)
	agencyID := insertAgency(t, pool, serverID, "1", nil)

	ids := []int64{
		insertBoundingBox(t, pool, v1, serverID, nil, 45.0, 46.0, -125.0, -124.0),
		insertBoundingBox(t, pool, v2, serverID, nil, 46.0, 47.0, -124.0, -123.0),
		insertBoundingBox(t, pool, v3, serverID, &agencyID, 47.0, 48.0, -123.0, -122.0),
	}
	// Extra unasked row
	v4 := insertStaticVersion(t, pool, serverID, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), nil, nil)
	insertBoundingBox(t, pool, v4, serverID, nil, 48.0, 49.0, -122.0, -121.0)

	router := newBoundingBoxVersionsRouter(t, pool)

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindBoundingBoxVersion](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != len(ids) {
		t.Errorf("loader returned %d versions, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		data, err := encodeJSON(got[id])
		if err != nil {
			t.Fatalf("encode bounding box version %d: %v", id, err)
		}
		rec := doGet(t, router, boundingBoxVersionPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("loader body != GET body for %d:\nloader: %s\nGET:    %s", id, data, rec.Body)
		}
	}
}
