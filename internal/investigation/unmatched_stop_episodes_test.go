package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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
	return router, calls
}

// allEpisodeKinds are the kinds behind every includable episode link.
var allEpisodeKinds = []resourceKind{"reported_stop", kindAgency, kindServer, kindOBAMetricsResponse, kindUnmatchedStopStaticContexts}

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
	for _, path := range []string{"/v1/unmatched-stop-episodes", "/v1/unmatched-stop-episodes/1"} {
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
