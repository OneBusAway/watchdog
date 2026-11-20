package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// episodesNow is the clock the episode tests run at.
var episodesNow = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

// fakeLoaders counts loader calls per kind.
type fakeLoaders map[resourceKind]int

// newEpisodesRouter mounts E14 and E15 at episodesNow with a fake loader for each kind.
func newEpisodesRouter(t *testing.T, pool *pgxpool.Pool, kinds ...resourceKind) (http.Handler, fakeLoaders) {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }
	calls := fakeLoaders{}
	for _, kind := range kinds {
		s.loaders[kind] = func(_ context.Context, _ *dbgen.Queries, ids []int64) (map[int64]any, error) {
			calls[kind]++
			out := make(map[int64]any, len(ids))
			for _, id := range ids {
				out[id] = map[string]any{"kind": string(kind), "id": id}
			}
			return out, nil
		}
	}
	router := httprouter.New()
	router.GET("/v1/unmatched-stop-episodes", s.listUnmatchedStopEpisodes)
	router.HEAD("/v1/unmatched-stop-episodes", s.listUnmatchedStopEpisodes)
	router.GET("/v1/unmatched-stop-episodes/:episode_id", s.getUnmatchedStopEpisode)
	router.HEAD("/v1/unmatched-stop-episodes/:episode_id", s.getUnmatchedStopEpisode)
	router.GET("/v1/unmatched-stop-episodes/:episode_id/static-contexts", s.listUnmatchedStopEpisodeStaticContexts)
	router.HEAD("/v1/unmatched-stop-episodes/:episode_id/static-contexts", s.listUnmatchedStopEpisodeStaticContexts)
	return router, calls
}

// allEpisodeKinds are the kinds behind every includable episode link.
var allEpisodeKinds = []resourceKind{kindReportedStop, kindAgency, kindServer, kindOBAMetricsResponse, kindUnmatchedStopStaticContexts}

// stop is one reported stop and the agency and server it belongs to.
type stop struct {
	pool                               *pgxpool.Pool
	serverID, agencyID, reportedStopID int64
}

func insertEpisodeServer(t *testing.T, pool *pgxpool.Pool, url string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ($1, 'Example') RETURNING server_id", url)
}

func insertEpisodeAgency(t *testing.T, pool *pgxpool.Pool, serverID int64, externalID string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO agency (server_id, external_agency_id) VALUES ($1, $2) RETURNING agency_id", serverID, externalID)
}

// addStop records stopID under agencyID.
func addStop(t *testing.T, pool *pgxpool.Pool, serverID, agencyID int64, stopID string) stop {
	t.Helper()
	id := dbtest.ID(t, pool,
		"INSERT INTO reported_stop (agency_id, oba_stop_id) VALUES ($1, $2) RETURNING reported_stop_id", agencyID, stopID)
	return stop{pool: pool, serverID: serverID, agencyID: agencyID, reportedStopID: id}
}

// newStop records stop 1_75403 of agency 1 on a new server.
func newStop(t *testing.T, pool *pgxpool.Pool) stop {
	t.Helper()
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	return addStop(t, pool, serverID, insertEpisodeAgency(t, pool, serverID, "1"), "1_75403")
}

// response records a metrics response observed at at and returns its ID and its agency row's ID.
func (s stop) response(t *testing.T, at time.Time) (responseID, responseAgencyID int64) {
	t.Helper()
	responseID = dbtest.ID(t, s.pool,
		"INSERT INTO onebusaway_metrics_response (server_id, observed_at) VALUES ($1, $2) RETURNING response_id",
		s.serverID, at)
	responseAgencyID = dbtest.ID(t, s.pool,
		"INSERT INTO onebusaway_metrics_response_agency (response_id, agency_id, response_complete) VALUES ($1, $2, true) RETURNING response_agency_id",
		responseID, s.agencyID)
	return responseID, responseAgencyID
}

// episode records an episode of s opened at opened and closed at closed, or still open when closed is zero.
func (s stop) episode(t *testing.T, opened, closed time.Time) int64 {
	t.Helper()
	_, first := s.response(t, opened)
	var closing *int64
	var reason *string
	if !closed.IsZero() {
		_, id := s.response(t, closed)
		closing = &id
		notReported := "not_reported"
		reason = &notReported
	}
	return dbtest.ID(t, s.pool,
		`INSERT INTO unmatched_stop_episode
		 (reported_stop_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING unmatched_stop_episode_id`,
		s.reportedStopID, s.agencyID, first, closing, opened.Add(30*time.Second), reason)
}

// linkStatic links episodeID to a static context published at published that did or did not find the stop.
func (s stop) linkStatic(t *testing.T, episodeID int64, published time.Time, found bool) {
	t.Helper()
	versionID := dbtest.ID(t, s.pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		s.serverID, published)
	metadataID := dbtest.ID(t, s.pool,
		`INSERT INTO stop_metadata_version (gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found)
		 VALUES ($1, $2, $3, $4) RETURNING stop_metadata_version_id`,
		versionID, s.agencyID, s.reportedStopID, found)
	if err := dbtest.Exec(s.pool,
		`INSERT INTO unmatched_stop_episode_metadata (unmatched_stop_episode_id, stop_metadata_version_id, reported_stop_id, agency_id)
		 VALUES ($1, $2, $3, $4)`,
		episodeID, metadataID, s.reportedStopID, s.agencyID); err != nil {
		t.Fatalf("link static context: %v", err)
	}
}

func episodePath(id int64) string {
	return fmt.Sprintf("/v1/unmatched-stop-episodes/%d", id)
}

// decodeEpisodeBody decodes rec's 200 body into a generic value.
func decodeEpisodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// listIDs returns the episode IDs of a list response, in order.
func listIDs(t *testing.T, body map[string]any) []int64 {
	t.Helper()
	ids := []int64{}
	for _, item := range body["items"].([]any) {
		ids = append(ids, int64(item.(map[string]any)["unmatched_stop_episode_id"].(float64)))
	}
	return ids
}

func wantIDs(t *testing.T, router http.Handler, path string, want ...int64) {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	if got := listIDs(t, decodeEpisodeBody(t, doGet(t, router, path, nil))); !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s returned episodes %v, want %v", path, got, want)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status = %d, want %d; body %s", rec.Code, want, rec.Body)
	}
}

// wantEpisodeHead checks HEAD path returns GET's status and headers.
func wantEpisodeHead(t *testing.T, router http.Handler, path string) {
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

func TestUnmatchedStopEpisodes_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, path := range []string{"/v1/unmatched-stop-episodes", "/v1/unmatched-stop-episodes/1", "/v1/unmatched-stop-episodes/1/static-contexts"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if h, _, _ := router.Lookup(method, path); h == nil {
				t.Errorf("%s %s is not registered", method, path)
			}
		}
	}
}

func TestGetUnmatchedStopEpisode_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.episode(t, t0, t0.Add(time.Hour))
	opened := t0.Add(2 * time.Hour)
	id := s.episode(t, opened, opened.Add(90*time.Second+500*time.Millisecond))
	router, _ := newEpisodesRouter(t, pool)

	var row dbgen.UnmatchedStopEpisodeView
	if err := pool.QueryRow(context.Background(),
		"SELECT opening_response_id, closing_response_id FROM unmatched_stop_episode_view WHERE unmatched_stop_episode_id = $1", id).
		Scan(&row.OpeningResponseID, &row.ClosingResponseID); err != nil {
		t.Fatalf("read response IDs: %v", err)
	}
	self := episodePath(id)
	want := map[string]any{
		"unmatched_stop_episode_id": float64(id),
		"status":                    "closed",
		"stop_id":                   "1_75403",
		"agency_id":                 float64(s.agencyID),
		"server_id":                 float64(s.serverID),
		"opened_at":                 "2026-10-01T02:00:00Z",
		"last_reported_at":          "2026-10-01T02:00:30Z",
		"closed_at":                 "2026-10-01T02:01:30.5Z",
		"duration_seconds":          float64(90),
		"close_reason":              "not_reported",
		"previous_episode_count":    float64(1),
		"previous_closed_at":        "2026-10-01T01:00:00Z",
		"links": map[string]any{
			"self":             self,
			"reported_stop":    fmt.Sprintf("/v1/reported-stops/%d", s.reportedStopID),
			"agency":           fmt.Sprintf("/v1/agencies/%d", s.agencyID),
			"server":           fmt.Sprintf("/v1/servers/%d", s.serverID),
			"opening_response": fmt.Sprintf("/v1/oba-metrics-responses/%d", row.OpeningResponseID),
			"closing_response": fmt.Sprintf("/v1/oba-metrics-responses/%d", *row.ClosingResponseID),
			"static_contexts":  self + "/static-contexts",
		},
	}
	if got := decodeEpisodeBody(t, doGet(t, router, self, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%v\nwant\n%v", got, want)
	}
}

func TestGetUnmatchedStopEpisode_OpenRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)

	body := decodeEpisodeBody(t, doGet(t, router, episodePath(id), nil))
	if body["status"] != "open" {
		t.Errorf("status = %v, want open", body["status"])
	}
	for _, name := range []string{"closed_at", "duration_seconds", "close_reason", "previous_closed_at"} {
		if v, ok := body[name]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", name, v, ok)
		}
	}
	if _, ok := body["links"].(map[string]any)["closing_response"]; ok {
		t.Error("open episode has a closing_response link")
	}
}

func TestGetUnmatchedStopEpisode_UnknownIDReturns404(t *testing.T) {
	router, _ := newEpisodesRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, episodePath(999), nil))
}

func TestGetUnmatchedStopEpisode_MalformedIDReturns404(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/unmatched-stop-episodes/"+id, nil))
		})
	}
}

func TestGetUnmatchedStopEpisode_RejectsUnknownParameter(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodePath(1)+"?foo=1", nil), "foo")
}

func TestGetUnmatchedStopEpisode_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)

	first := doGet(t, router, episodePath(id), nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, episodePath(id), map[string]string{"If-None-Match": tag})
	wantStatus(t, second, http.StatusNotModified)
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetUnmatchedStopEpisode_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	closedAt := episodesNow.Add(-time.Hour)
	closed := s.episode(t, episodesNow.Add(-2*time.Hour), closedAt)
	open := s.episode(t, episodesNow.Add(-time.Minute), time.Time{})
	router, _ := newEpisodesRouter(t, pool, kindAgency)

	for _, tc := range []struct {
		path, cacheControl, lastModified string
	}{
		{episodePath(closed), "private, max-age=86400", closedAt.Format(http.TimeFormat)},
		{episodePath(closed) + "?include=agency", "private, max-age=30", ""},
		{episodePath(open), "private, max-age=30", ""},
	} {
		rec := doGet(t, router, tc.path, nil)
		wantStatus(t, rec, http.StatusOK)
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.cacheControl)
		}
		if got := rec.Header().Get("Last-Modified"); got != tc.lastModified {
			t.Errorf("%s: Last-Modified = %q, want %q", tc.path, got, tc.lastModified)
		}
	}
}

func TestGetUnmatchedStopEpisode_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantEpisodeHead(t, router, episodePath(id))
}

func TestGetUnmatchedStopEpisode_IncludeEach(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	router, calls := newEpisodesRouter(t, pool, allEpisodeKinds...)

	body := decodeEpisodeBody(t, doGet(t, router,
		episodePath(id)+"?include=reported_stop,agency,server,opening_response,closing_response,static_contexts", nil))
	got := body["included"].(map[string]any)
	l := body["links"].(map[string]any)
	if len(got) != 6 {
		t.Errorf("included has %d entries, want 6: %v", len(got), got)
	}
	for _, name := range []string{"reported_stop", "agency", "server", "opening_response", "closing_response", "static_contexts"} {
		if _, ok := got[l[name].(string)]; !ok {
			t.Errorf("included has no entry for %s (%s)", name, l[name])
		}
	}
	if calls[kindOBAMetricsResponse] != 1 {
		t.Errorf("response loader called %d times, want 1", calls[kindOBAMetricsResponse])
	}
}

func TestGetUnmatchedStopEpisode_IncludeAbsentTarget(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool, allEpisodeKinds...)

	body := decodeEpisodeBody(t, doGet(t, router, episodePath(id)+"?include=closing_response", nil))
	if got := body["included"].(map[string]any); len(got) != 0 {
		t.Errorf("included = %v, want empty", got)
	}
}

func TestGetUnmatchedStopEpisode_IncludeRejectsUnknown(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil, allEpisodeKinds...)
	for _, include := range []string{"foo", "agency,agency", ""} {
		t.Run(include, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, episodePath(1)+"?include="+include, nil), "include")
		})
	}
}

func TestGetUnmatchedStopEpisode_NoIncludedKeyByDefault(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool, allEpisodeKinds...)

	if _, ok := decodeEpisodeBody(t, doGet(t, router, episodePath(id), nil))["included"]; ok {
		t.Error("body has an included key")
	}
}

const episodesPath = "/v1/unmatched-stop-episodes"

func TestListUnmatchedStopEpisodes_ReturnsNewestFirst(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	a := s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour))
	b := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	c := s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath, c, b, a)
}

func TestListUnmatchedStopEpisodes_Paginates(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	a := s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour))
	b := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	c := s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)

	first := decodeEpisodeBody(t, doGet(t, router, episodesPath+"?page_size=2", nil))
	if got := listIDs(t, first); !reflect.DeepEqual(got, []int64{c, b}) {
		t.Errorf("first page = %v, want %v", got, []int64{c, b})
	}
	next, ok := first["links"].(map[string]any)["next"].(string)
	if !ok {
		t.Fatal("first page has no links.next")
	}
	second := decodeEpisodeBody(t, doGet(t, router, next, nil))
	if got := listIDs(t, second); !reflect.DeepEqual(got, []int64{a}) {
		t.Errorf("second page = %v, want %v", got, []int64{a})
	}
	if _, ok := second["links"].(map[string]any)["next"]; ok {
		t.Error("last page has links.next")
	}
}

func TestListUnmatchedStopEpisodes_EmptyResult(t *testing.T) {
	router, _ := newEpisodesRouter(t, dbtest.NewMigratedPool(t))
	body := decodeEpisodeBody(t, doGet(t, router, episodesPath, nil))
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", body["items"])
	}
	if _, ok := body["links"].(map[string]any)["next"]; ok {
		t.Error("empty page has links.next")
	}
}

func TestListUnmatchedStopEpisodes_RejectsBadPageSize(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	for _, size := range []string{"0", "201"} {
		t.Run(size, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, episodesPath+"?page_size="+size, nil), "page_size")
		})
	}
}

func TestListUnmatchedStopEpisodes_RejectsBadCursor(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantStatus(t, doGet(t, router, episodesPath+"?cursor=%%%", nil), http.StatusBadRequest)
}

func TestListUnmatchedStopEpisodes_Filter_server_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverA := insertEpisodeServer(t, pool, "https://a.example.com")
	serverB := insertEpisodeServer(t, pool, "https://b.example.com")
	a := addStop(t, pool, serverA, insertEpisodeAgency(t, pool, serverA, "1"), "1_75403").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	addStop(t, pool, serverB, insertEpisodeAgency(t, pool, serverB, "1"), "1_75403").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, fmt.Sprintf("%s?server_id=%d", episodesPath, serverA), a)
}

func TestListUnmatchedStopEpisodes_Filter_agency_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyA := insertEpisodeAgency(t, pool, serverID, "1")
	a := addStop(t, pool, serverID, agencyA, "1_75403").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	addStop(t, pool, serverID, insertEpisodeAgency(t, pool, serverID, "40"), "1_75403").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, fmt.Sprintf("%s?agency_id=%d", episodesPath, agencyA), a)
}

func TestListUnmatchedStopEpisodes_Filter_stop_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	a := s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	addStop(t, pool, s.serverID, s.agencyID, "1_99999").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, fmt.Sprintf("%s?agency_id=%d&stop_id=1_75403", episodesPath, s.agencyID), a)
}

func TestListUnmatchedStopEpisodes_Filter_status(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	closed := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	open := s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath+"?status=open", open)
	wantIDs(t, router, episodesPath+"?status=closed", closed)
}

func TestListUnmatchedStopEpisodes_Filter_close_reason(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	closed := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath+"?close_reason=not_reported", closed)
}

func TestListUnmatchedStopEpisodes_RejectsUnknownParameter(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?foo=1", nil), "foo")
}

func TestListUnmatchedStopEpisodes_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantEpisodeHead(t, router, episodesPath)
}

func TestListUnmatchedStopEpisodes_CachesShort(t *testing.T) {
	router, _ := newEpisodesRouter(t, dbtest.NewMigratedPool(t))
	if got := doGet(t, router, episodesPath, nil).Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
}

// windowQuery is a list path with the active window [from, to).
func windowQuery(from, to time.Time) string {
	return episodesPath + "?" + url.Values{
		"active_from": {from.Format(time.RFC3339)},
		"active_to":   {to.Format(time.RFC3339)},
	}.Encode()
}

func TestListUnmatchedStopEpisodes_WindowOverlap(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	from, to := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	s.episode(t, from.Add(-3*time.Hour), from.Add(-2*time.Hour))
	overlapping := s.episode(t, from.Add(-time.Hour), from.Add(time.Hour))
	stillOpen := s.episode(t, from.Add(-5*time.Hour), time.Time{})
	s.episode(t, to.Add(time.Hour), to.Add(2*time.Hour))
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, windowQuery(from, to), stillOpen, overlapping)
}

func TestListUnmatchedStopEpisodes_WindowNeedsBothEnds(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?active_from=2026-10-01T00:00:00Z", nil), "active_to")
	same := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, windowQuery(same, same), nil), "active_to")
}

func TestListUnmatchedStopEpisodes_WindowRequiresOffset(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router,
		episodesPath+"?active_from=2026-10-01T00:00:00&active_to=2026-10-02T00:00:00Z", nil), "active_from")
}

func TestListUnmatchedStopEpisodes_WindowDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	s.episode(t, episodesNow.Add(-26*time.Hour), episodesNow.Add(-25*time.Hour))
	recent := s.episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath, recent)
}

func TestListUnmatchedStopEpisodes_WindowMax31Days(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, windowQuery(from, from.Add(32*24*time.Hour)), nil), "active_to")
}

func TestListUnmatchedStopEpisodes_OpenStatusIgnoresDefaultWindow(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-40*24*time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath+"?status=open", id)
}

func TestListUnmatchedStopEpisodes_StopIDRequiresAgency(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?stop_id=1_75403", nil), "stop_id")
}

func TestListUnmatchedStopEpisodes_StaticFound(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	published := episodesNow.Add(-48 * time.Hour)
	found := s.episode(t, episodesNow.Add(-3*time.Hour), time.Time{})
	s.linkStatic(t, found, published, false)
	s.linkStatic(t, found, published.Add(time.Hour), true)
	other := addStop(t, pool, s.serverID, s.agencyID, "1_99999")
	notFound := other.episode(t, episodesNow.Add(-2*time.Hour), time.Time{})
	other.linkStatic(t, notFound, published, false)
	addStop(t, pool, s.serverID, s.agencyID, "1_88888").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, episodesPath+"?static_found=true", found)
	wantIDs(t, router, episodesPath+"?static_found=false", notFound)
}

func TestListUnmatchedStopEpisodes_IncludeStaticContexts(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour))
	s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, calls := newEpisodesRouter(t, pool, kindUnmatchedStopStaticContexts)

	body := decodeEpisodeBody(t, doGet(t, router, episodesPath+"?include=static_contexts", nil))
	got := body["included"].(map[string]any)
	if len(got) != 3 {
		t.Errorf("included has %d entries, want 3: %v", len(got), got)
	}
	for _, item := range body["items"].([]any) {
		u := item.(map[string]any)["links"].(map[string]any)["static_contexts"].(string)
		if _, ok := got[u]; !ok {
			t.Errorf("included has no entry for %s", u)
		}
	}
	if calls[kindUnmatchedStopStaticContexts] != 1 {
		t.Errorf("loader called %d times, want 1", calls[kindUnmatchedStopStaticContexts])
	}
}

func TestListUnmatchedStopEpisodes_BadCloseReason(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?close_reason=matched", nil), "close_reason")
}

func (s stop) linkStaticVersion(t *testing.T, episodeID int64, published time.Time, found bool) (versionID, metadataID int64) {
	t.Helper()
	versionID = dbtest.ID(t, s.pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		s.serverID, published)
	metadataID = dbtest.ID(t, s.pool,
		`INSERT INTO stop_metadata_version (gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found)
		 VALUES ($1, $2, $3, $4) RETURNING stop_metadata_version_id`,
		versionID, s.agencyID, s.reportedStopID, found)
	if err := dbtest.Exec(s.pool,
		`INSERT INTO unmatched_stop_episode_metadata (unmatched_stop_episode_id, stop_metadata_version_id, reported_stop_id, agency_id)
		 VALUES ($1, $2, $3, $4)`,
		episodeID, metadataID, s.reportedStopID, s.agencyID); err != nil {
		t.Fatalf("link static context: %v", err)
	}
	return versionID, metadataID
}

func (s stop) episodeWithResponses(t *testing.T, opened, closed time.Time) (epID, openResp, closeResp int64) {
	t.Helper()
	openResp, first := s.response(t, opened)
	var closing *int64
	var reason *string
	if !closed.IsZero() {
		var id int64
		closeResp, id = s.response(t, closed)
		closing = &id
		notReported := "not_reported"
		reason = &notReported
	}
	epID = dbtest.ID(t, s.pool,
		`INSERT INTO unmatched_stop_episode
		 (reported_stop_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING unmatched_stop_episode_id`,
		s.reportedStopID, s.agencyID, first, closing, opened.Add(30*time.Second), reason)
	return epID, openResp, closeResp
}

func staticContextsPath(id int64) string {
	return episodePath(id) + "/static-contexts"
}

func TestListUnmatchedStopEpisodes_Filter_opened_window(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	// Opened before window, still open -> excluded
	s.episode(t, episodesNow.Add(-10*time.Hour), time.Time{})
	// Opened inside window -> included
	inWindow := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	router, _ := newEpisodesRouter(t, pool)

	from := episodesNow.Add(-5 * time.Hour).Format(time.RFC3339)
	to := episodesNow.Add(-time.Hour).Format(time.RFC3339)
	wantIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", episodesPath, from, to), inWindow)

	// Window pair rules
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s", episodesPath, from), nil), "opened_to")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_to=%s", episodesPath, to), nil), "opened_from")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", episodesPath, to, from), nil), "opened_to")
	over31 := episodesNow.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", episodesPath, over31, to), nil), "opened_to")
}

func TestListUnmatchedStopEpisodes_Filter_open_at(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	tInstant := episodesNow.Add(-10 * time.Hour)

	// Ep 1: closed before tInstant
	s.episode(t, tInstant.Add(-3*time.Hour), tInstant.Add(-time.Hour))
	// Ep 2: open across tInstant
	openAcross := s.episode(t, tInstant.Add(-time.Hour), tInstant.Add(time.Hour))
	// Ep 3: opened after tInstant
	s.episode(t, tInstant.Add(time.Hour), tInstant.Add(3*time.Hour))

	router, _ := newEpisodesRouter(t, pool)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s", episodesPath, tInstant.Format(time.RFC3339)), openAcross)

	// Future open_at returns 400
	future := episodesNow.Add(time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?open_at=%s", episodesPath, future), nil), "open_at")
}

func TestListUnmatchedStopEpisodes_NeitherWindowKeepsDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	// Opened 40 days ago, closed 30 days ago
	oldEp := s.episode(t, episodesNow.Add(-40*24*time.Hour), episodesNow.Add(-30*24*time.Hour))
	router, _ := newEpisodesRouter(t, pool)

	// open_at inside the open period (35 days ago) returns it
	at := episodesNow.Add(-35 * 24 * time.Hour).Format(time.RFC3339)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s", episodesPath, at), oldEp)

	// opened window covering 40 days ago returns it
	from := episodesNow.Add(-41 * 24 * time.Hour).Format(time.RFC3339)
	to := episodesNow.Add(-39 * 24 * time.Hour).Format(time.RFC3339)
	wantIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", episodesPath, from, to), oldEp)
}

func TestListUnmatchedStopEpisodes_Filter_responses_and_static(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	ep1, openResp1, closeResp1 := s.episodeWithResponses(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	ep2, _, _ := s.episodeWithResponses(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))

	v1, _ := s.linkStaticVersion(t, ep1, episodesNow.Add(-10*time.Hour), true)
	v2, _ := s.linkStaticVersion(t, ep2, episodesNow.Add(-5*time.Hour), true)

	router, _ := newEpisodesRouter(t, pool)

	// opening_response_id
	wantIDs(t, router, fmt.Sprintf("%s?opening_response_id=%d", episodesPath, openResp1), ep1)
	// closing_response_id
	wantIDs(t, router, fmt.Sprintf("%s?closing_response_id=%d", episodesPath, closeResp1), ep1)
	// gtfs_static_version_id
	wantIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", episodesPath, v1), ep1)
	wantIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", episodesPath, v2), ep2)
}

func TestListUnmatchedStopEpisodes_Filter_recurrence(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	ep1 := s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour))
	ep2 := s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour))
	router, _ := newEpisodesRouter(t, pool)

	wantIDs(t, router, episodesPath+"?recurrence=has_previous_episode", ep2)
	wantIDs(t, router, episodesPath+"?recurrence=no_previous_episode", ep1)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?recurrence=unknown", nil), "recurrence")
}

func TestListUnmatchedStopEpisodes_AgencyNone_Returns400(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, episodesPath+"?agency_id=none", nil), "agency_id")
}

func TestListUnmatchedStopEpisodes_ClusterFilters(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)

	// Set up cluster scheme and cells
	dbtest.Exec(pool,
		"INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at) VALUES ($1, 12, 'none', 'desc', $2)",
		"scheme-1", episodesNow.Add(-100*time.Hour))
	dbtest.Exec(pool,
		"INSERT INTO s2_cell (scheme_version, cell_id, cell_level, center_latitude, center_longitude) VALUES ($1, 'cell-a', 12, 47.0, -122.0), ($1, 'cell-b', 12, 47.1, -122.1)",
		"scheme-1")

	// Episode opened at -5h, closed at -1h
	epID := s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-time.Hour))

	// Static context 1: published at -10h -> cell-a, station-1
	_, meta1 := s.linkStaticVersion(t, epID, episodesNow.Add(-10*time.Hour), true)
	st1 := "station-1"
	dbtest.Exec(pool,
		"INSERT INTO stop_cluster_assignment (stop_metadata_version_id, scheme_version, cell_id, root_station_id, root_station_name) VALUES ($1, $2, $3, $4, 'Station 1')",
		meta1, "scheme-1", "cell-a", &st1)

	// Static context 2: published at -3h -> cell-b, station-2
	_, meta2 := s.linkStaticVersion(t, epID, episodesNow.Add(-3*time.Hour), true)
	st2 := "station-2"
	dbtest.Exec(pool,
		"INSERT INTO stop_cluster_assignment (stop_metadata_version_id, scheme_version, cell_id, root_station_id, root_station_name) VALUES ($1, $2, $3, $4, 'Station 2')",
		meta2, "scheme-1", "cell-b", &st2)

	router, _ := newEpisodesRouter(t, pool)

	// At -4h: meta1 applies (cell-a, station-1)
	tEarly := episodesNow.Add(-4 * time.Hour).Format(time.RFC3339)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&cell_id=cell-a", episodesPath, tEarly), epID)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&cell_id=cell-b", episodesPath, tEarly))
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&root_station_id=station-1", episodesPath, tEarly), epID)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&root_station_id=station-2", episodesPath, tEarly))

	// At -2h: meta2 applies (cell-b, station-2)
	tLate := episodesNow.Add(-2 * time.Hour).Format(time.RFC3339)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&cell_id=cell-b", episodesPath, tLate), epID)
	wantIDs(t, router, fmt.Sprintf("%s?open_at=%s&cell_id=cell-a", episodesPath, tLate))

	// Absent open_at: latest applies (cell-b)
	wantIDs(t, router, fmt.Sprintf("%s?cell_id=cell-b", episodesPath), epID)
	wantIDs(t, router, fmt.Sprintf("%s?cell_id=cell-a", episodesPath))

	// Unknown scheme_version returns 400
	wantInvalidParam(t, doGet(t, router, episodesPath+"?scheme_version=nonexistent&cell_id=cell-a", nil), "scheme_version")
}

// staticContextItems returns the items of GET staticContextsPath(id).
func staticContextItems(t *testing.T, router http.Handler, id int64) []any {
	t.Helper()
	return decodeEpisodeBody(t, doGet(t, router, staticContextsPath(id), nil))["items"].([]any)
}

func TestListUnmatchedStopEpisodeStaticContexts_RejectsUnknownParameter(t *testing.T) {
	router, _ := newEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, staticContextsPath(1)+"?foo=1", nil), "foo")
}

func TestListUnmatchedStopEpisodeStaticContexts_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	id := s.episode(t, episodesNow.Add(-time.Hour), time.Time{})
	s.linkStatic(t, id, episodesNow.Add(-2*time.Hour), true)
	router, _ := newEpisodesRouter(t, pool)
	wantEpisodeHead(t, router, staticContextsPath(id))
}

func TestListUnmatchedStopEpisodeStaticContexts_AppliesRule(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	opened, during, closed := episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour)
	closedID := s.episode(t, opened, closed)
	other := addStop(t, pool, s.serverID, s.agencyID, "1_75404")
	openID := other.episode(t, opened, time.Time{})
	for _, st := range []struct {
		stop stop
		id   int64
	}{{s, closedID}, {other, openID}} {
		st.stop.linkStatic(t, st.id, opened.Add(-24*time.Hour), true)
		st.stop.linkStatic(t, st.id, during, true)
	}
	router, _ := newEpisodesRouter(t, pool)

	format := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	for _, tc := range []struct {
		id        int64
		lastUntil any
	}{{closedID, format(closed)}, {openID, nil}} {
		items := staticContextItems(t, router, tc.id)
		if len(items) != 2 {
			t.Fatalf("episode %d: %d items, want 2", tc.id, len(items))
		}
		first, second := items[0].(map[string]any), items[1].(map[string]any)
		if first["applies_from"] != format(opened) || first["applies_until"] != format(during) {
			t.Errorf("episode %d: first applies %v to %v, want %s to %s",
				tc.id, first["applies_from"], first["applies_until"], format(opened), format(during))
		}
		if second["applies_from"] != format(during) || second["applies_until"] != tc.lastUntil {
			t.Errorf("episode %d: second applies %v to %v, want %s to %v",
				tc.id, second["applies_from"], second["applies_until"], format(during), tc.lastUntil)
		}
	}
}

func TestListUnmatchedStopEpisodeStaticContexts_NotFoundRow(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	id := s.episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	s.linkStatic(t, id, episodesNow.Add(-3*time.Hour), false)
	router, _ := newEpisodesRouter(t, pool)

	item := staticContextItems(t, router, id)[0].(map[string]any)
	if item["static_stop_found"] != false || item["source"] != "watchdog_static_gtfs" {
		t.Errorf("static_stop_found = %v, source = %v", item["static_stop_found"], item["source"])
	}
	for _, field := range []string{"stop_name", "latitude", "longitude", "parent_station_id", "location_type"} {
		if v, ok := item[field]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", field, v, ok)
		}
	}
}

func TestListUnmatchedStopEpisodeStaticContexts_Empty(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newStop(t, pool).episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router, _ := newEpisodesRouter(t, pool)

	body := decodeEpisodeBody(t, doGet(t, router, staticContextsPath(id), nil))
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", body["items"])
	}
	want := map[string]any{"self": staticContextsPath(id), "episode": episodePath(id)}
	if !reflect.DeepEqual(body["links"], want) {
		t.Errorf("links = %v, want %v", body["links"], want)
	}
}

func TestListUnmatchedStopEpisodeStaticContexts_UnknownEpisode(t *testing.T) {
	router, _ := newEpisodesRouter(t, dbtest.NewMigratedPool(t))
	for _, id := range []string{"999", "abc", "0"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/unmatched-stop-episodes/"+id+"/static-contexts", nil))
		})
	}
}

func TestStopStaticContextsLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	s := newStop(t, pool)
	ids := []int64{
		s.episode(t, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour)),
		s.episode(t, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour)),
		s.episode(t, episodesNow.Add(-time.Hour), time.Time{}),
	}
	s.linkStatic(t, ids[0], episodesNow.Add(-6*time.Hour), true)
	s.linkStatic(t, ids[0], episodesNow.Add(-270*time.Minute), false)
	s.linkStatic(t, ids[2], episodesNow.Add(-2*time.Hour), true)
	router, _ := newEpisodesRouter(t, pool)

	svc, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := svc.loaders[kindUnmatchedStopStaticContexts](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != len(ids) {
		t.Errorf("loader returned %d episodes, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		data, err := encodeJSON(got[id])
		if err != nil {
			t.Fatalf("encode episode %d: %v", id, err)
		}
		rec := doGet(t, router, staticContextsPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET episode %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("episode %d: loaded\n%s\nGET returned\n%s", id, data, rec.Body)
		}
	}
}

func TestListUnmatchedStopEpisodes_FollowClusterEpisodesLink(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	seedCell(t, pool, "s", "c2")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opened := at.Add(-time.Hour)

	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	var c1IDs, c2IDs []int64
	for i := range 4 {
		st := addStop(t, pool, serverID, agencyID, fmt.Sprintf("1_%d", i))
		id := st.episode(t, opened, time.Time{})
		if i < 3 {
			assign(t, pool, linkVersion(t, st, id, opened.Add(-time.Hour)), "s", "c1", strPtr("1_A"))
			c1IDs = append(c1IDs, id)
		} else {
			assign(t, pool, linkVersion(t, st, id, opened.Add(-time.Hour)), "s", "c2", nil)
			c2IDs = append(c2IDs, id)
		}
	}

	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }
	router := httprouter.New()
	router.GET("/v1/unmatched-stop-clusters", s.listUnmatchedStopClusters)
	router.GET("/v1/unmatched-stop-episodes", s.listUnmatchedStopEpisodes)

	clustersURL := fmt.Sprintf("/v1/unmatched-stop-clusters?server_id=%d&at=%s&scheme_version=s",
		serverID, url.QueryEscape(at.Format(time.RFC3339Nano)))
	clustersRec := doGet(t, router, clustersURL, nil)
	if clustersRec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", clustersURL, clustersRec.Code)
	}
	body := decodeEpisodeBody(t, clustersRec)
	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("cluster items = %v, want 2 items", body["items"])
	}

	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		linksMap, _ := item["links"].(map[string]any)
		episodesLink, ok := linksMap["episodes"].(string)
		if !ok || episodesLink == "" {
			t.Fatalf("missing links.episodes on item: %v", item)
		}

		epRec := doGet(t, router, episodesLink, nil)
		if epRec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body = %s", episodesLink, epRec.Code, epRec.Body.String())
		}
		epBody := decodeEpisodeBody(t, epRec)
		gotItems, _ := epBody["items"].([]any)
		var gotIDs []int64
		for _, rawEp := range gotItems {
			ep := rawEp.(map[string]any)
			gotIDs = append(gotIDs, int64(ep["unmatched_stop_episode_id"].(float64)))
		}

		cellID := item["cell_id"].(string)
		var wantIDs []int64
		if cellID == "c1" {
			wantIDs = c1IDs
		} else {
			wantIDs = c2IDs
		}
		slices.Sort(gotIDs)
		slices.Sort(wantIDs)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("cluster %s episodes = %v, want %v", cellID, gotIDs, wantIDs)
		}
	}
}
