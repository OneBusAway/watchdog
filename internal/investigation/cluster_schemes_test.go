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
)

// newClusterSchemesRouter returns a router serving list and get cluster-scheme
// handlers for GET and HEAD on a service using pool (which may be nil for
// tests that never reach the database).
//
// The handlers are mounted without the request pipeline so unit tests are not
// rejected by auth. The pipeline's own checks (401, 405, 406, 503) are tested
// in routes_test.go and pipeline_test.go.
func newClusterSchemesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool

	router := httprouter.New()
	router.GET("/v1/cluster-schemes", s.listClusterSchemes)
	router.HEAD("/v1/cluster-schemes", s.listClusterSchemes)
	router.GET("/v1/cluster-schemes/:scheme_version", s.getClusterScheme)
	router.HEAD("/v1/cluster-schemes/:scheme_version", s.getClusterScheme)
	return router
}

func TestGetClusterScheme_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/cluster-schemes"); h == nil {
			t.Errorf("%s /v1/cluster-schemes is not registered", method)
		}
		if h, _, _ := router.Lookup(method, "/v1/cluster-schemes/v1"); h == nil {
			t.Errorf("%s /v1/cluster-schemes/:scheme_version is not registered", method)
		}
	}
}

func TestGetClusterScheme_Representation(t *testing.T) {
	created := time.Date(2026, 10, 24, 14, 5, 30, 0, time.UTC)
	row := dbgen.ClusterScheme{
		SchemeVersion:   "v1",
		S2Level:         14,
		StationGrouping: "none",
		Description:     "Test description",
		CreatedAt:       created,
	}
	rep := clusterSchemeRepresentation(row)
	if rep.SchemeVersion != "v1" {
		t.Errorf("scheme_version = %q, want v1", rep.SchemeVersion)
	}
	if rep.S2Level != 14 {
		t.Errorf("s2_level = %d, want 14", rep.S2Level)
	}
	if rep.StationGrouping != "none" {
		t.Errorf("station_grouping = %q, want none", rep.StationGrouping)
	}
	if rep.Description != "Test description" {
		t.Errorf("description = %q, want Test description", rep.Description)
	}
	if rep.Links["self"] != "/v1/cluster-schemes/v1" {
		t.Errorf("links.self = %q, want /v1/cluster-schemes/v1", rep.Links["self"])
	}
}

func TestGetClusterScheme_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	created := time.Date(2026, 10, 24, 14, 5, 30, 123456000, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"2024-01", 12, "none", "Standard cluster scheme", created); err != nil {
		t.Fatalf("insert cluster scheme: %v", err)
	}
	router := newClusterSchemesRouter(t, pool)

	rec := doGet(t, router, "/v1/cluster-schemes/2024-01", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Error("ETag is empty")
	}
	want := `{"scheme_version":"2024-01","s2_level":12,"station_grouping":"none","description":"Standard cluster scheme",` +
		`"created_at":"2026-10-24T14:05:30.123456Z","links":{"self":"/v1/cluster-schemes/2024-01"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestGetClusterScheme_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClusterSchemesRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/cluster-schemes/nonexistent-scheme", nil))
}

func TestGetClusterScheme_MalformedIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClusterSchemesRouter(t, pool)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/cluster-schemes/"+id, nil))
		})
	}
}

func TestGetClusterScheme_RejectsUnknownParameter(t *testing.T) {
	router := newClusterSchemesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/cluster-schemes/2024-01?foo=1", nil), "foo")
}

func TestGetClusterScheme_RejectsInclude(t *testing.T) {
	router := newClusterSchemesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/cluster-schemes/2024-01?include=anything", nil), "include")
}

func TestGetClusterScheme_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	created := time.Date(2026, 10, 24, 14, 5, 30, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"etag-test", 12, "none", "ETag test", created); err != nil {
		t.Fatalf("insert cluster scheme: %v", err)
	}
	router := newClusterSchemesRouter(t, pool)

	get := doGet(t, router, "/v1/cluster-schemes/etag-test", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("response has no ETag")
	}

	revalidate := doGet(t, router, "/v1/cluster-schemes/etag-test", map[string]string{
		"If-None-Match": etag,
	})
	if revalidate.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", revalidate.Code)
	}
	if revalidate.Body.Len() > 0 {
		t.Errorf("304 has body: %s", revalidate.Body)
	}
}

func TestGetClusterScheme_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	created := time.Date(2026, 10, 24, 14, 5, 30, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"head-test", 12, "none", "Head test", created); err != nil {
		t.Fatalf("insert cluster scheme: %v", err)
	}
	router := newClusterSchemesRouter(t, pool)

	get := doGet(t, router, "/v1/cluster-schemes/head-test", nil)
	req := httptest.NewRequest(http.MethodHead, "/v1/cluster-schemes/head-test", nil)
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
	if got := head.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
}

func TestListClusterSchemes_RejectsUnknownParameter(t *testing.T) {
	router := newClusterSchemesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/cluster-schemes?foo=1", nil), "foo")
	// The list is not paginated, so the page parameters are unknown here too.
	wantInvalidParam(t, doGet(t, router, "/v1/cluster-schemes?page_size=10", nil), "page_size")
	wantInvalidParam(t, doGet(t, router, "/v1/cluster-schemes?cursor=abc", nil), "cursor")
}

// Schemes inserted with the same created_at must list in the same order every
// time, so the strong ETag stays the same for an unchanged list.
func TestListClusterSchemes_TiedCreatedAtIsStable(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, version := range []string{"alpha", "beta", "gamma"} {
		if err := dbtest.Exec(pool,
			"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
			version, 12, "none", "Scheme "+version, created); err != nil {
			t.Fatalf("insert %s: %v", version, err)
		}
	}
	router := newClusterSchemesRouter(t, pool)

	first := doGet(t, router, "/v1/cluster-schemes", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}
	wantOrder := []string{"gamma", "beta", "alpha"}
	var body struct {
		Items []struct {
			SchemeVersion string `json:"scheme_version"`
		} `json:"items"`
	}
	if err := json.NewDecoder(first.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	var got []string
	for _, item := range body.Items {
		got = append(got, item.SchemeVersion)
	}
	if !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("order = %v, want %v", got, wantOrder)
	}

	etag := first.Header().Get("ETag")
	for range 5 {
		again := doGet(t, router, "/v1/cluster-schemes", nil)
		if again.Header().Get("ETag") != etag {
			t.Fatalf("ETag changed between identical requests: %q vs %q", again.Header().Get("ETag"), etag)
		}
	}
}

func TestListClusterSchemes_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	created := time.Date(2026, 10, 24, 14, 5, 30, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"head-list", 12, "none", "Head list", created); err != nil {
		t.Fatalf("insert cluster scheme: %v", err)
	}
	router := newClusterSchemesRouter(t, pool)

	get := doGet(t, router, "/v1/cluster-schemes", nil)
	req := httptest.NewRequest(http.MethodHead, "/v1/cluster-schemes", nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
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

func TestListClusterSchemes_Order(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	older := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"older", 10, "none", "Older scheme", older); err != nil {
		t.Fatalf("insert older scheme: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, $2, $3, $4, $5)",
		"newer", 14, "parent_station", "Newer scheme", newer); err != nil {
		t.Fatalf("insert newer scheme: %v", err)
	}
	router := newClusterSchemesRouter(t, pool)

	rec := doGet(t, router, "/v1/cluster-schemes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := map[string]any{
		"items": []any{
			map[string]any{
				"scheme_version":   "newer",
				"s2_level":         float64(14),
				"station_grouping": "parent_station",
				"description":      "Newer scheme",
				"created_at":       "2026-10-02T00:00:00Z",
				"links":            map[string]any{"self": "/v1/cluster-schemes/newer"},
			},
			map[string]any{
				"scheme_version":   "older",
				"s2_level":         float64(10),
				"station_grouping": "none",
				"description":      "Older scheme",
				"created_at":       "2026-10-01T00:00:00Z",
				"links":            map[string]any{"self": "/v1/cluster-schemes/older"},
			},
		},
		"links": map[string]any{"self": "/v1/cluster-schemes"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body =\n%v\nwant\n%v", body, want)
	}
}
