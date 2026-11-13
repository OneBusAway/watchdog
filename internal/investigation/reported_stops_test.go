package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newReportedStopsRouter mounts E08, E09 and getAgency without the request pipeline.
func newReportedStopsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return nil }

	router := httprouter.New()
	router.GET("/v1/reported-stops", s.listReportedStops)
	router.HEAD("/v1/reported-stops", s.listReportedStops)
	router.GET("/v1/reported-stops/:reported_stop_id", s.getReportedStop)
	router.HEAD("/v1/reported-stops/:reported_stop_id", s.getReportedStop)
	router.GET("/v1/agencies/:agency_id", s.getAgency)
	return router
}

// insertReportedStop records a reported stop and returns its ID.
func insertReportedStop(t *testing.T, pool *pgxpool.Pool, agencyID int64, stopID string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO reported_stop (agency_id, oba_stop_id) VALUES ($1, $2) RETURNING reported_stop_id",
		agencyID, stopID)
}

// insertReportedStopFixture records stops 900 (agency 40) and 901 (agency 41), both 1_75403.
func insertReportedStopFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, stmt := range []string{
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')",
		"INSERT INTO agency (agency_id, server_id, external_agency_id) OVERRIDING SYSTEM VALUE VALUES (40, 7, '1'), (41, 7, '40')",
		"INSERT INTO reported_stop (reported_stop_id, agency_id, oba_stop_id) OVERRIDING SYSTEM VALUE VALUES (900, 40, '1_75403'), (901, 41, '1_75403')",
	} {
		if err := dbtest.Exec(pool, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func reportedStopsQuery(agencyID int64, stopID string) string {
	return "/v1/reported-stops?agency_id=" + strconv.FormatInt(agencyID, 10) + "&stop_id=" + stopID
}

func reportedStopPath(id int64) string {
	return "/v1/reported-stops/" + strconv.FormatInt(id, 10)
}

// decodeJSON decodes a 200 body into a map of its top-level fields.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return raw
}

func TestReportedStops_Registered(t *testing.T) {
	s, router := newTestService(t)
	for _, path := range []string{"/v1/reported-stops", "/v1/reported-stops/1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if h, _, _ := router.Lookup(method, path); h == nil {
				t.Errorf("%s %s is not registered", method, path)
			}
		}
	}
	if _, ok := s.loaders[kindReportedStop]; !ok {
		t.Error("no loader registered for reported stops")
	}
}

func TestListReportedStops_RequiresBoth(t *testing.T) {
	// No database: a missing parameter must be rejected before any query.
	router := newReportedStopsRouter(t, nil)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"agency_id", "stop_id"}},
		{"?agency_id=40", []string{"stop_id"}},
		{"?stop_id=1_75403", []string{"agency_id"}},
		{"?include=agency", []string{"agency_id", "stop_id"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := doGet(t, router, "/v1/reported-stops"+tc.query, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
			var body problem
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			var got []string
			for _, p := range body.InvalidParams {
				if p.Reason != "is required" {
					t.Errorf("%s: reason = %q, want is required", p.Name, p.Reason)
				}
				got = append(got, p.Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("invalid_params names = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListReportedStops_RejectsUnknownParameter(t *testing.T) {
	router := newReportedStopsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, reportedStopsQuery(40, "1_75403")+"&foo=1", nil), "foo")
}

func TestListReportedStops_RejectsInvalidValues(t *testing.T) {
	router := newReportedStopsRouter(t, nil)
	for _, tc := range []struct{ query, name string }{
		{"agency_id=&stop_id=1_75403", "agency_id"},
		{"agency_id=0&stop_id=1_75403", "agency_id"},
		{"agency_id=-1&stop_id=1_75403", "agency_id"},
		{"agency_id=abc&stop_id=1_75403", "agency_id"},
		{"agency_id=040&stop_id=1_75403", "agency_id"},
		{"agency_id=40&agency_id=41&stop_id=1_75403", "agency_id"},
		{"agency_id=40&stop_id=", "stop_id"},
		{"agency_id=40&stop_id=a&stop_id=b", "stop_id"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/reported-stops?"+tc.query, nil), tc.name)
		})
	}
}

func TestListReportedStops_RejectsBadInclude(t *testing.T) {
	router := newReportedStopsRouter(t, nil)
	for _, include := range []string{"self", "reported_stop", "unmatched_stop_episodes", "", "agency,agency"} {
		t.Run(include, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, reportedStopsQuery(40, "1_75403")+"&include="+include, nil), "include")
		})
	}
}

func TestListReportedStops_Lookup(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertReportedStopFixture(t, pool)
	router := newReportedStopsRouter(t, pool)

	// The same OBA stop ID in agency 41 must not be returned.
	rec := doGet(t, router, "/v1/reported-stops?stop_id=1_75403&agency_id=40", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	want := `{"items":[` +
		`{"reported_stop_id":900,"agency_id":40,"stop_id":"1_75403",` +
		`"links":{"agency":"/v1/agencies/40","self":"/v1/reported-stops/900",` +
		`"unmatched_stop_episodes":"/v1/reported-stops/900/unmatched-stop-episodes"}}],` +
		`"links":{"self":"/v1/reported-stops?agency_id=40&stop_id=1_75403"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestListReportedStops_NoMatch(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertReportedStopFixture(t, pool)
	router := newReportedStopsRouter(t, pool)

	for _, path := range []string{
		reportedStopsQuery(40, "1_99999"),
		reportedStopsQuery(999999, "1_75403"),
	} {
		raw := decodeJSON(t, doGet(t, router, path, nil))
		if got := string(raw["items"]); got != "[]" {
			t.Errorf("%s: items = %s, want []", path, got)
		}
		if _, ok := raw["included"]; ok {
			t.Errorf("%s: body has included without an include parameter", path)
		}
	}

	raw := decodeJSON(t, doGet(t, router, reportedStopsQuery(40, "1_99999")+"&include=agency", nil))
	if got := string(raw["included"]); got != "{}" {
		t.Errorf("included = %s, want {}", got)
	}
}

func TestListReportedStops_IncludeAgency(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	agencyID := insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	insertReportedStop(t, pool, agencyID, "1_75403")
	router := newReportedStopsRouter(t, pool)

	var included map[string]json.RawMessage
	raw := decodeJSON(t, doGet(t, router, reportedStopsQuery(agencyID, "1_75403")+"&include=agency", nil))
	if err := json.Unmarshal(raw["included"], &included); err != nil {
		t.Fatalf("decode included: %v", err)
	}
	if len(included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(included), included)
	}
	path := agencyPath(agencyID)
	get := doGet(t, router, path, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", path, get.Code)
	}
	if got := string(included[path]) + "\n"; got != get.Body.String() {
		t.Errorf("included agency =\n%s\nGET returned\n%s", got, get.Body)
	}
}

func TestGetReportedStop_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertReportedStopFixture(t, pool)
	router := newReportedStopsRouter(t, pool)

	rec := doGet(t, router, "/v1/reported-stops/901", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	want := `{"reported_stop_id":901,"agency_id":41,"stop_id":"1_75403",` +
		`"links":{"agency":"/v1/agencies/41","self":"/v1/reported-stops/901",` +
		`"unmatched_stop_episodes":"/v1/reported-stops/901/unmatched-stop-episodes"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestGetReportedStop_MalformedIDReturns404(t *testing.T) {
	// No database: a malformed ID must be rejected before any query.
	router := newReportedStopsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/reported-stops/"+id, nil))
		})
	}
}

func TestGetReportedStop_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertReportedStopFixture(t, pool)
	router := newReportedStopsRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/reported-stops/999999", nil))
}

func TestGetReportedStop_RejectsUnknownParameter(t *testing.T) {
	router := newReportedStopsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/reported-stops/1?foo=1", nil), "foo")
	wantInvalidParam(t, doGet(t, router, "/v1/reported-stops/1?agency_id=40", nil), "agency_id")
}

func TestGetReportedStop_RejectsBadInclude(t *testing.T) {
	router := newReportedStopsRouter(t, nil)
	for _, include := range []string{"self", "reported_stop", "unmatched_stop_episodes", "", "agency,agency"} {
		t.Run(include, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/reported-stops/1?include="+include, nil), "include")
		})
	}
}

func TestGetReportedStop_IncludeAgency(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	agencyID := insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	id := insertReportedStop(t, pool, agencyID, "1_75403")
	router := newReportedStopsRouter(t, pool)

	rec := doGet(t, router, reportedStopPath(id)+"?include=agency", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		ReportedStopID int64                      `json:"reported_stop_id"`
		Included       map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.ReportedStopID != id {
		t.Errorf("reported_stop_id = %d, want %d", body.ReportedStopID, id)
	}
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(body.Included), body.Included)
	}
	path := agencyPath(agencyID)
	get := doGet(t, router, path, nil)
	if got := string(body.Included[path]) + "\n"; got != get.Body.String() {
		t.Errorf("included agency =\n%s\nGET returned\n%s", got, get.Body)
	}
}

// newReportedStopRow records one reported stop and returns its E08 and E09 paths.
func newReportedStopRow(t *testing.T) (router http.Handler, listPath, getPath string) {
	t.Helper()
	pool := dbtest.NewMigratedPool(t)
	agencyID := insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	id := insertReportedStop(t, pool, agencyID, "1_75403")
	return newReportedStopsRouter(t, pool), reportedStopsQuery(agencyID, "1_75403"), reportedStopPath(id)
}

// wantCached checks Cache-Control, no Last-Modified, and a 304 on a repeated ETag.
func wantCached(t *testing.T, router http.Handler, path, cacheControl string) {
	t.Helper()
	first := doGet(t, router, path, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	if got := first.Header().Get("Cache-Control"); got != cacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, cacheControl)
	}
	if got := first.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none", got)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, path, map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

// wantHeadMatchesGet checks HEAD path returns GET's status and headers.
func wantHeadMatchesGet(t *testing.T, router http.Handler, path string) {
	t.Helper()
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

func TestListReportedStops_ETag(t *testing.T) {
	router, listPath, _ := newReportedStopRow(t)
	wantCached(t, router, listPath, "private, max-age=30")
}

func TestGetReportedStop_ETag(t *testing.T) {
	router, _, getPath := newReportedStopRow(t)
	wantCached(t, router, getPath, "private, max-age=86400")
}

func TestListReportedStops_Head(t *testing.T) {
	router, listPath, _ := newReportedStopRow(t)
	wantHeadMatchesGet(t, router, listPath)
}

func TestGetReportedStop_Head(t *testing.T) {
	router, _, getPath := newReportedStopRow(t)
	wantHeadMatchesGet(t, router, getPath)
}

func TestReportedStopLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	a := insertAgency(t, pool, insertServer(t, pool, "https://a.example.com", "A"), "1", nil)
	b := insertAgency(t, pool, insertServer(t, pool, "https://b.example.com", "B"), "1", nil)
	ids := []int64{
		insertReportedStop(t, pool, a, "1_75403"),
		insertReportedStop(t, pool, a, "1_75404"),
		insertReportedStop(t, pool, b, "1_75403"),
	}
	insertReportedStop(t, pool, b, "not-asked")
	router := newReportedStopsRouter(t, pool)

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindReportedStop](context.Background(), dbgen.New(db), append(ids, 999999))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != len(ids) {
		t.Errorf("loader returned %d reported stops, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		data, err := encodeJSON(got[id])
		if err != nil {
			t.Fatalf("encode reported stop %d: %v", id, err)
		}
		rec := doGet(t, router, reportedStopPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET reported stop %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("reported stop %d: loaded\n%s\nGET returned\n%s", id, data, rec.Body)
		}
	}
}
