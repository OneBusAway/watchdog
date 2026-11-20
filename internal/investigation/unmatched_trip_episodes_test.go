package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// tripEpisodesNow is the clock the trip-episode detail tests run at.
var tripEpisodesNow = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

// newTripEpisodesRouter mounts E18 with optional fake loaders.
func newTripEpisodesRouter(t *testing.T, pool *pgxpool.Pool, kinds ...resourceKind) (http.Handler, fakeLoaders) {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
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
	router.GET("/v1/unmatched-trip-episodes/:episode_id", s.getUnmatchedTripEpisode)
	router.HEAD("/v1/unmatched-trip-episodes/:episode_id", s.getUnmatchedTripEpisode)
	return router, calls
}

// allTripEpisodeKinds are the kinds behind every includable trip-episode link.
var allTripEpisodeKinds = []resourceKind{
	kindReportedTrip, kindAgency, kindServer, kindOBAMetricsResponse, kindUnmatchedTripStaticContexts,
}

// newTripFixture records trip Trip_A of agency 1 on a new server (reuses episode_counts trip helpers).
func newTripFixture(t *testing.T, pool *pgxpool.Pool) trip {
	t.Helper()
	serverID := insertEpisodeServer(t, pool, "https://trip.example.com")
	return addTrip(t, pool, serverID, insertEpisodeAgency(t, pool, serverID, "1"), "Trip_A")
}

func tripEpisodePath(id int64) string {
	return fmt.Sprintf("/v1/unmatched-trip-episodes/%d", id)
}

func decodeTripEpisodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
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

func TestUnmatchedTripEpisodes_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/unmatched-trip-episodes/1"); h == nil {
			t.Errorf("%s /v1/unmatched-trip-episodes/:episode_id is not registered", method)
		}
	}
}

func TestGetUnmatchedTripEpisode_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tr.episode(t, t0, t0.Add(time.Hour))
	opened := t0.Add(2 * time.Hour)
	id := tr.episode(t, opened, opened.Add(90*time.Second+500*time.Millisecond))
	router, _ := newTripEpisodesRouter(t, pool)

	var row dbgen.UnmatchedTripEpisodeView
	if err := pool.QueryRow(context.Background(),
		"SELECT opening_response_id, closing_response_id FROM unmatched_trip_episode_view WHERE unmatched_trip_episode_id = $1", id).
		Scan(&row.OpeningResponseID, &row.ClosingResponseID); err != nil {
		t.Fatalf("read response IDs: %v", err)
	}
	self := tripEpisodePath(id)
	want := map[string]any{
		"unmatched_trip_episode_id": float64(id),
		"status":                    "closed",
		"trip_id":                   "Trip_A",
		"agency_id":                 float64(tr.agencyID),
		"server_id":                 float64(tr.serverID),
		"opened_at":                 "2026-10-01T02:00:00Z",
		"last_reported_at":          "2026-10-01T02:00:30Z",
		"closed_at":                 "2026-10-01T02:01:30.5Z",
		"duration_seconds":          float64(90),
		"close_reason":              "not_reported",
		"previous_episode_count":    float64(1),
		"previous_closed_at":        "2026-10-01T01:00:00Z",
		"links": map[string]any{
			"self":             self,
			"reported_trip":    fmt.Sprintf("/v1/reported-trips/%d", tr.reportedTripID),
			"agency":           fmt.Sprintf("/v1/agencies/%d", tr.agencyID),
			"server":           fmt.Sprintf("/v1/servers/%d", tr.serverID),
			"opening_response": fmt.Sprintf("/v1/oba-metrics-responses/%d", row.OpeningResponseID),
			"closing_response": fmt.Sprintf("/v1/oba-metrics-responses/%d", *row.ClosingResponseID),
			"static_contexts":  self + "/static-contexts",
		},
	}
	if got := decodeTripEpisodeBody(t, doGet(t, router, self, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%v\nwant\n%v", got, want)
	}
}

func TestGetUnmatchedTripEpisode_OpenRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodesRouter(t, pool)

	body := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodePath(id), nil))
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

func TestGetUnmatchedTripEpisode_UnknownIDReturns404(t *testing.T) {
	router, _ := newTripEpisodesRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, tripEpisodePath(999), nil))
}

func TestGetUnmatchedTripEpisode_MalformedIDReturns404(t *testing.T) {
	router, _ := newTripEpisodesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/unmatched-trip-episodes/"+id, nil))
		})
	}
}

func TestGetUnmatchedTripEpisode_RejectsUnknownParameter(t *testing.T) {
	router, _ := newTripEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodePath(1)+"?foo=1", nil), "foo")
}

func TestGetUnmatchedTripEpisode_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodesRouter(t, pool)

	first := doGet(t, router, tripEpisodePath(id), nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, tripEpisodePath(id), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304; body %s", second.Code, second.Body)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetUnmatchedTripEpisode_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	closedAt := tripEpisodesNow.Add(-time.Hour)
	closed := tr.episode(t, tripEpisodesNow.Add(-2*time.Hour), closedAt)
	open := tr.episode(t, tripEpisodesNow.Add(-time.Minute), time.Time{})
	router, _ := newTripEpisodesRouter(t, pool, kindAgency)

	for _, tc := range []struct {
		path, cacheControl, lastModified string
	}{
		{tripEpisodePath(closed), "private, max-age=86400", closedAt.Format(http.TimeFormat)},
		{tripEpisodePath(closed) + "?include=agency", "private, max-age=30", ""},
		{tripEpisodePath(open), "private, max-age=30", ""},
	} {
		rec := doGet(t, router, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.cacheControl)
		}
		if got := rec.Header().Get("Last-Modified"); got != tc.lastModified {
			t.Errorf("%s: Last-Modified = %q, want %q", tc.path, got, tc.lastModified)
		}
	}
}

func TestGetUnmatchedTripEpisode_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	handler, _ := newTripEpisodesRouter(t, pool)
	server := httptest.NewServer(handler)
	defer server.Close()
	url := server.URL + tripEpisodePath(id)

	get, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	head, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	defer head.Body.Close()

	if get.StatusCode != http.StatusOK || head.StatusCode != get.StatusCode {
		t.Fatalf("GET %d HEAD %d", get.StatusCode, head.StatusCode)
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag"} {
		if head.Header.Get(key) != get.Header.Get(key) {
			t.Errorf("%s: HEAD %q, GET %q", key, head.Header.Get(key), get.Header.Get(key))
		}
	}
	data, err := io.ReadAll(head.Body)
	if err != nil || len(data) != 0 {
		t.Errorf("HEAD body %q, error %v", data, err)
	}
	getBody, err := io.ReadAll(get.Body)
	if err != nil || len(getBody) == 0 {
		t.Errorf("GET body empty or error %v", err)
	}
}

func TestGetUnmatchedTripEpisode_IncludeEach(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-2*time.Hour), tripEpisodesNow.Add(-time.Hour))
	router, calls := newTripEpisodesRouter(t, pool, allTripEpisodeKinds...)

	body := decodeTripEpisodeBody(t, doGet(t, router,
		tripEpisodePath(id)+"?include=reported_trip,agency,server,opening_response,closing_response,static_contexts", nil))
	got := body["included"].(map[string]any)
	l := body["links"].(map[string]any)
	if len(got) != 6 {
		t.Errorf("included has %d entries, want 6: %v", len(got), got)
	}
	for _, name := range []string{"reported_trip", "agency", "server", "opening_response", "closing_response", "static_contexts"} {
		if _, ok := got[l[name].(string)]; !ok {
			t.Errorf("included has no entry for %s (%s)", name, l[name])
		}
	}
	if calls[kindOBAMetricsResponse] != 1 {
		t.Errorf("response loader called %d times, want 1", calls[kindOBAMetricsResponse])
	}
}

func TestGetUnmatchedTripEpisode_IncludeAbsentTarget(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodesRouter(t, pool, allTripEpisodeKinds...)

	body := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodePath(id)+"?include=closing_response", nil))
	if got := body["included"].(map[string]any); len(got) != 0 {
		t.Errorf("included = %v, want empty", got)
	}
}

func TestGetUnmatchedTripEpisode_IncludeRejectsUnknown(t *testing.T) {
	router, _ := newTripEpisodesRouter(t, nil, allTripEpisodeKinds...)
	for _, include := range []string{"foo", "agency,agency", ""} {
		t.Run(include, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, tripEpisodePath(1)+"?include="+include, nil), "include")
		})
	}
}

func TestGetUnmatchedTripEpisode_NoIncludedKeyByDefault(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodesRouter(t, pool, allTripEpisodeKinds...)

	if _, ok := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodePath(id), nil))["included"]; ok {
		t.Error("body has an included key")
	}
}

// newTripStaticRouter mounts E19 at episodesNow.
func newTripStaticRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }
	router := httprouter.New()
	router.GET("/v1/unmatched-trip-episodes/:episode_id/static-contexts", s.listUnmatchedTripEpisodeStaticContexts)
	router.HEAD("/v1/unmatched-trip-episodes/:episode_id/static-contexts", s.listUnmatchedTripEpisodeStaticContexts)
	return router
}

// tripStaticFixture is one trip with its own server and agency, for the E19 tests.
func tripStaticFixture(t *testing.T, pool *pgxpool.Pool, tripID string) trip {
	t.Helper()
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	return addTrip(t, pool, serverID, insertEpisodeAgency(t, pool, serverID, "1"), tripID)
}

// headRequest sends a HEAD request and returns the recorded response.
func headRequest(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
	return rec
}

// linkStatic records a static trip context for the episode: a static version published at published, and whether the trip was found in it.
func (tr trip) linkStatic(t *testing.T, episodeID int64, published time.Time, found bool) {
	t.Helper()
	versionID := dbtest.ID(t, tr.pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		tr.serverID, published)
	var route, service, block *string
	if found {
		r, s, b := "route-1", "service-1", "block-1"
		route, service, block = &r, &s, &b
	}
	metadataID := dbtest.ID(t, tr.pool,
		`INSERT INTO trip_metadata_version
		 (gtfs_static_version_id, agency_id, reported_trip_id, static_trip_found, route_id, service_id, block_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING trip_metadata_version_id`,
		versionID, tr.agencyID, tr.reportedTripID, found, route, service, block)
	if err := dbtest.Exec(tr.pool,
		`INSERT INTO unmatched_trip_episode_metadata (unmatched_trip_episode_id, trip_metadata_version_id, reported_trip_id, agency_id)
		 VALUES ($1, $2, $3, $4)`,
		episodeID, metadataID, tr.reportedTripID, tr.agencyID); err != nil {
		t.Fatalf("link static context: %v", err)
	}
}

func tripStaticContextsPath(id int64) string {
	return fmt.Sprintf("/v1/unmatched-trip-episodes/%d/static-contexts", id)
}

// tripStaticContextItems returns the items of GET tripStaticContextsPath(id).
func tripStaticContextItems(t *testing.T, router http.Handler, id int64) []any {
	t.Helper()
	return decodeEpisodeBody(t, doGet(t, router, tripStaticContextsPath(id), nil))["items"].([]any)
}

func TestListUnmatchedTripEpisodeStaticContexts_RejectsUnknownParameter(t *testing.T) {
	router := newTripStaticRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripStaticContextsPath(1)+"?foo=1", nil), "foo")
}

func TestListUnmatchedTripEpisodeStaticContexts_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := tripStaticFixture(t, pool, "trip-1").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router := newTripStaticRouter(t, pool)

	get := doGet(t, router, tripStaticContextsPath(id), nil)
	head := headRequest(t, router, tripStaticContextsPath(id))
	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	for _, h := range []string{"Cache-Control", "ETag", "Content-Type"} {
		if head.Header().Get(h) != get.Header().Get(h) {
			t.Errorf("HEAD %s = %q, GET %s = %q", h, head.Header().Get(h), h, get.Header().Get(h))
		}
	}
	// net/http drops a HEAD body itself, so only the headers are compared here.
}

func TestListUnmatchedTripEpisodeStaticContexts_AppliesRule(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := tripStaticFixture(t, pool, "trip-1")
	opened, during, closed := episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour)
	closedID := tr.episode(t, opened, closed)
	other := addTrip(t, pool, tr.serverID, tr.agencyID, "trip-2")
	openID := other.episode(t, opened, time.Time{})
	for _, st := range []struct {
		trip trip
		id   int64
	}{{tr, closedID}, {other, openID}} {
		st.trip.linkStatic(t, st.id, opened.Add(-24*time.Hour), true)
		st.trip.linkStatic(t, st.id, during, true)
	}
	router := newTripStaticRouter(t, pool)

	format := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	for _, tc := range []struct {
		id        int64
		lastUntil any
	}{{closedID, format(closed)}, {openID, nil}} {
		items := tripStaticContextItems(t, router, tc.id)
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

func TestListUnmatchedTripEpisodeStaticContexts_NotFoundRow(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := tripStaticFixture(t, pool, "trip-1")
	id := tr.episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	tr.linkStatic(t, id, episodesNow.Add(-3*time.Hour), false)
	router := newTripStaticRouter(t, pool)

	item := tripStaticContextItems(t, router, id)[0].(map[string]any)
	if item["static_trip_found"] != false || item["source"] != "watchdog_static_gtfs" {
		t.Errorf("static_trip_found = %v, source = %v", item["static_trip_found"], item["source"])
	}
	for _, field := range []string{"route_id", "service_id", "block_id"} {
		if v, ok := item[field]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", field, v, ok)
		}
	}
}

func TestListUnmatchedTripEpisodeStaticContexts_UnknownEpisode(t *testing.T) {
	router := newTripStaticRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, tripStaticContextsPath(999), nil))
}

func TestListUnmatchedTripEpisodeStaticContexts_Empty(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := tripStaticFixture(t, pool, "trip-1").episode(t, episodesNow.Add(-time.Hour), time.Time{})
	router := newTripStaticRouter(t, pool)

	body := decodeEpisodeBody(t, doGet(t, router, tripStaticContextsPath(id), nil))
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want an empty list", body["items"])
	}
}

func TestTripStaticContextsLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := tripStaticFixture(t, pool, "trip-1")
	ids := make([]int64, 0, 3)
	for i := range 3 {
		id := tr.episode(t, episodesNow.Add(-time.Duration(i+2)*time.Hour), episodesNow.Add(-time.Duration(i+1)*time.Hour))
		tr.linkStatic(t, id, episodesNow.Add(-time.Duration(i+3)*time.Hour), true)
		ids = append(ids, id)
	}
	router := newTripStaticRouter(t, pool)

	s, _ := newTestService(t)
	s.Pool = pool
	var loaded map[int64]any
	err := s.read(context.Background(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		loaded, err = loadTripStaticContexts(ctx, q, ids)
		return err
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, id := range ids {
		want := decodeEpisodeBody(t, doGet(t, router, tripStaticContextsPath(id), nil))
		raw, err := json.Marshal(loaded[id])
		if err != nil {
			t.Fatalf("marshal loader value: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode loader value: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("episode %d: loader value =\n%v\nwant endpoint body\n%v", id, got, want)
		}
	}
}

// E18's static_contexts include uses the E19 loader, so requesting it returns one entry keyed by that link.
func TestGetUnmatchedTripEpisode_IncludeStaticContexts(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := tripStaticFixture(t, pool, "trip-1")
	id := tr.episode(t, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour))
	tr.linkStatic(t, id, episodesNow.Add(-3*time.Hour), true)
	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }
	router := httprouter.New()
	router.GET("/v1/unmatched-trip-episodes/:episode_id", s.getUnmatchedTripEpisode)

	body := decodeEpisodeBody(t, doGet(t, router, tripEpisodePath(id)+"?include=static_contexts", nil))
	included, _ := body["included"].(map[string]any)
	want := tripStaticContextsPath(id)
	if _, ok := included[want]; !ok || len(included) != 1 {
		t.Errorf("included = %v, want exactly one entry keyed %s", included, want)
	}
}

const tripEpisodesPath = "/v1/unmatched-trip-episodes"

// newTripEpisodeListRouter mounts E17 and E18 at tripEpisodesNow with a fake loader for each kind.
func newTripEpisodeListRouter(t *testing.T, pool *pgxpool.Pool, kinds ...resourceKind) (http.Handler, fakeLoaders) {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return tripEpisodesNow }
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
	router.GET("/v1/unmatched-trip-episodes", s.listUnmatchedTripEpisodes)
	router.HEAD("/v1/unmatched-trip-episodes", s.listUnmatchedTripEpisodes)
	router.GET("/v1/unmatched-trip-episodes/:episode_id", s.getUnmatchedTripEpisode)
	return router, calls
}

// tripWindowQuery is the E17 path with the active window [from, to).
func tripWindowQuery(from, to time.Time) string {
	return tripEpisodesPath + "?" + url.Values{
		"active_from": {from.Format(time.RFC3339)},
		"active_to":   {to.Format(time.RFC3339)},
	}.Encode()
}

// tripEpisodeIDs lists the unmatched_trip_episode_id of each item in body.
func tripEpisodeIDs(t *testing.T, body map[string]any) []int64 {
	t.Helper()
	ids := []int64{}
	for _, item := range body["items"].([]any) {
		ids = append(ids, int64(item.(map[string]any)["unmatched_trip_episode_id"].(float64)))
	}
	return ids
}

// wantTripEpisodeIDs checks that GET path lists exactly want, in order.
func wantTripEpisodeIDs(t *testing.T, router http.Handler, path string, want ...int64) {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	if got := tripEpisodeIDs(t, decodeTripEpisodeBody(t, doGet(t, router, path, nil))); !reflect.DeepEqual(got, want) {
		t.Errorf("GET %s returned episodes %v, want %v", path, got, want)
	}
}

func TestListUnmatchedTripEpisodes_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, tripEpisodesPath); h == nil {
			t.Errorf("%s %s is not registered", method, tripEpisodesPath)
		}
	}
}

func TestListUnmatchedTripEpisodes_ReturnsNewestFirst(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	a := tr.episode(t, tripEpisodesNow.Add(-5*time.Hour), tripEpisodesNow.Add(-4*time.Hour))
	b := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	c := tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath, c, b, a)
}

// TestListUnmatchedTripEpisodes_ItemMatchesDetail checks that an item is
// exactly the E18 representation of the same episode.
func TestListUnmatchedTripEpisodes_ItemMatchesDetail(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)

	items := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodesPath, nil))["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, item := range items {
		got := item.(map[string]any)
		id := int64(got["unmatched_trip_episode_id"].(float64))
		if want := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodePath(id), nil)); !reflect.DeepEqual(got, want) {
			t.Errorf("episode %d: item\n%v\ndetail\n%v", id, got, want)
		}
	}
}

func TestListUnmatchedTripEpisodes_Paginates(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	a := tr.episode(t, tripEpisodesNow.Add(-5*time.Hour), tripEpisodesNow.Add(-4*time.Hour))
	b := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	c := tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)

	first := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodesPath+"?page_size=2", nil))
	if got := tripEpisodeIDs(t, first); !reflect.DeepEqual(got, []int64{c, b}) {
		t.Errorf("first page = %v, want %v", got, []int64{c, b})
	}
	next, ok := first["links"].(map[string]any)["next"].(string)
	if !ok {
		t.Fatal("first page has no links.next")
	}
	second := decodeTripEpisodeBody(t, doGet(t, router, next, nil))
	if got := tripEpisodeIDs(t, second); !reflect.DeepEqual(got, []int64{a}) {
		t.Errorf("second page = %v, want %v", got, []int64{a})
	}
	if _, ok := second["links"].(map[string]any)["next"]; ok {
		t.Error("last page has links.next")
	}
}

func TestListUnmatchedTripEpisodes_EmptyResult(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, dbtest.NewMigratedPool(t))
	body := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodesPath, nil))
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", body["items"])
	}
	if _, ok := body["links"].(map[string]any)["next"]; ok {
		t.Error("empty result has links.next")
	}
}

func TestListUnmatchedTripEpisodes_RejectsBadPageSize(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	for _, size := range []string{"0", "201"} {
		wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?page_size="+size, nil), "page_size")
	}
}

func TestListUnmatchedTripEpisodes_RejectsBadCursor(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?cursor=%25%25%25", nil), "cursor")
}

func TestListUnmatchedTripEpisodes_Filter_server_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverA := insertEpisodeServer(t, pool, "https://a.example.com")
	serverB := insertEpisodeServer(t, pool, "https://b.example.com")
	a := addTrip(t, pool, serverA, insertEpisodeAgency(t, pool, serverA, "1"), "Trip_A").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	addTrip(t, pool, serverB, insertEpisodeAgency(t, pool, serverB, "1"), "Trip_A").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?server_id=%d", tripEpisodesPath, serverA), a)
}

func TestListUnmatchedTripEpisodes_Filter_agency_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyA := insertEpisodeAgency(t, pool, serverID, "1")
	a := addTrip(t, pool, serverID, agencyA, "Trip_A").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	addTrip(t, pool, serverID, insertEpisodeAgency(t, pool, serverID, "40"), "Trip_A").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?agency_id=%d", tripEpisodesPath, agencyA), a)
}

func TestListUnmatchedTripEpisodes_Filter_trip_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	a := tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	addTrip(t, pool, tr.serverID, tr.agencyID, "Trip_B").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?agency_id=%d&trip_id=Trip_A", tripEpisodesPath, tr.agencyID), a)
}

func TestListUnmatchedTripEpisodes_Filter_status(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	closed := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	open := tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?status=open", open)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?status=closed", closed)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?status=other", nil), "status")
}

func TestListUnmatchedTripEpisodes_Filter_close_reason(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	closed := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?close_reason=not_reported", closed)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?close_reason=fresh_source_timestamp", nil), "close_reason")
}

func TestListUnmatchedTripEpisodes_RejectsUnknownParameter(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?foo=1", nil), "foo")
}

func TestListUnmatchedTripEpisodes_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	newTripFixture(t, pool).episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantEpisodeHead(t, router, tripEpisodesPath)
	get := doGet(t, router, tripEpisodesPath, nil)
	if got := get.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if get.Header().Get("ETag") == "" {
		t.Error("no ETag header")
	}
}

func TestListUnmatchedTripEpisodes_WindowOverlap(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	from, to := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	tr.episode(t, from.Add(-3*time.Hour), from.Add(-2*time.Hour))
	overlapping := tr.episode(t, from.Add(-time.Hour), from.Add(time.Hour))
	stillOpen := tr.episode(t, from.Add(-5*time.Hour), time.Time{})
	tr.episode(t, to.Add(time.Hour), to.Add(2*time.Hour))
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripWindowQuery(from, to), stillOpen, overlapping)
}

func TestListUnmatchedTripEpisodes_WindowNeedsBothEnds(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?active_from=2026-10-01T00:00:00Z", nil), "active_to")
	same := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, tripWindowQuery(same, same), nil), "active_to")
}

func TestListUnmatchedTripEpisodes_WindowRequiresOffset(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router,
		tripEpisodesPath+"?active_from=2026-10-01T00:00:00&active_to=2026-10-02T00:00:00Z", nil), "active_from")
}

func TestListUnmatchedTripEpisodes_WindowDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tr.episode(t, tripEpisodesNow.Add(-26*time.Hour), tripEpisodesNow.Add(-25*time.Hour))
	recent := tr.episode(t, tripEpisodesNow.Add(-2*time.Hour), tripEpisodesNow.Add(-time.Hour))
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath, recent)
}

func TestListUnmatchedTripEpisodes_WindowMax31Days(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, tripWindowQuery(from, from.Add(32*24*time.Hour)), nil), "active_to")
}

func TestListUnmatchedTripEpisodes_OpenStatusIgnoresDefaultWindow(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tr.episode(t, tripEpisodesNow.Add(-41*24*time.Hour), tripEpisodesNow.Add(-40*24*time.Hour))
	id := tr.episode(t, tripEpisodesNow.Add(-40*24*time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?status=open", id)
}

func TestListUnmatchedTripEpisodes_TripIDRequiresAgency(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?trip_id=Trip_A", nil), "trip_id")
	// An empty trip_id is reported once, as empty, not a second time as needing agency_id.
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?trip_id=", nil), "trip_id")
}

func TestListUnmatchedTripEpisodes_StaticFound(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	published := tripEpisodesNow.Add(-48 * time.Hour)
	found := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), time.Time{})
	tr.linkStatic(t, found, published, false)
	tr.linkStatic(t, found, published.Add(time.Hour), true)
	other := addTrip(t, pool, tr.serverID, tr.agencyID, "Trip_B")
	notFound := other.episode(t, tripEpisodesNow.Add(-2*time.Hour), time.Time{})
	other.linkStatic(t, notFound, published, false)
	addTrip(t, pool, tr.serverID, tr.agencyID, "Trip_C").episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?static_found=true", found)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?static_found=false", notFound)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?static_found=yes", nil), "static_found")
}

func TestListUnmatchedTripEpisodes_IncludeStaticContexts(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tr.episode(t, tripEpisodesNow.Add(-5*time.Hour), tripEpisodesNow.Add(-4*time.Hour))
	tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	tr.episode(t, tripEpisodesNow.Add(-time.Hour), time.Time{})
	router, calls := newTripEpisodeListRouter(t, pool, kindUnmatchedTripStaticContexts)

	body := decodeTripEpisodeBody(t, doGet(t, router, tripEpisodesPath+"?include=static_contexts", nil))
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
	if calls[kindUnmatchedTripStaticContexts] != 1 {
		t.Errorf("loader called %d times, want 1", calls[kindUnmatchedTripStaticContexts])
	}
}

func (tr trip) linkStaticWithRoute(t *testing.T, episodeID int64, published time.Time, found bool, routeID *string) (versionID, metadataID int64) {
	t.Helper()
	versionID = dbtest.ID(t, tr.pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		tr.serverID, published)
	var service, block *string
	if found {
		s, b := "service-1", "block-1"
		service, block = &s, &b
	}
	metadataID = dbtest.ID(t, tr.pool,
		`INSERT INTO trip_metadata_version
		 (gtfs_static_version_id, agency_id, reported_trip_id, static_trip_found, route_id, service_id, block_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING trip_metadata_version_id`,
		versionID, tr.agencyID, tr.reportedTripID, found, routeID, service, block)
	if err := dbtest.Exec(tr.pool,
		`INSERT INTO unmatched_trip_episode_metadata (unmatched_trip_episode_id, trip_metadata_version_id, reported_trip_id, agency_id)
		 VALUES ($1, $2, $3, $4)`,
		episodeID, metadataID, tr.reportedTripID, tr.agencyID); err != nil {
		t.Fatalf("link static context: %v", err)
	}
	return versionID, metadataID
}

func (tr trip) episodeWithResponses(t *testing.T, opened, closed time.Time) (epID, openResp, closeResp int64) {
	t.Helper()
	openResp, first := tr.response(t, opened)
	var closing *int64
	var reason *string
	if !closed.IsZero() {
		var id int64
		closeResp, id = tr.response(t, closed)
		closing = &id
		notReported := "not_reported"
		reason = &notReported
	}
	epID = dbtest.ID(t, tr.pool,
		`INSERT INTO unmatched_trip_episode
		 (reported_trip_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING unmatched_trip_episode_id`,
		tr.reportedTripID, tr.agencyID, first, closing, opened.Add(30*time.Second), reason)
	return epID, openResp, closeResp
}

func TestListUnmatchedTripEpisodes_Filter_opened_window(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tr.episode(t, tripEpisodesNow.Add(-10*time.Hour), time.Time{})
	inWindow := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	router, _ := newTripEpisodeListRouter(t, pool)

	from := tripEpisodesNow.Add(-5 * time.Hour).Format(time.RFC3339)
	to := tripEpisodesNow.Add(-time.Hour).Format(time.RFC3339)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", tripEpisodesPath, from, to), inWindow)

	// Window pair rules
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s", tripEpisodesPath, from), nil), "opened_to")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_to=%s", tripEpisodesPath, to), nil), "opened_from")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", tripEpisodesPath, to, from), nil), "opened_to")
	over31 := tripEpisodesNow.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", tripEpisodesPath, over31, to), nil), "opened_to")
}

func TestListUnmatchedTripEpisodes_Filter_open_at(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	tInstant := tripEpisodesNow.Add(-10 * time.Hour)

	tr.episode(t, tInstant.Add(-3*time.Hour), tInstant.Add(-time.Hour))
	openAcross := tr.episode(t, tInstant.Add(-time.Hour), tInstant.Add(time.Hour))
	tr.episode(t, tInstant.Add(time.Hour), tInstant.Add(3*time.Hour))

	router, _ := newTripEpisodeListRouter(t, pool)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?open_at=%s", tripEpisodesPath, tInstant.Format(time.RFC3339)), openAcross)

	// Future open_at returns 400
	future := tripEpisodesNow.Add(time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?open_at=%s", tripEpisodesPath, future), nil), "open_at")
}

func TestListUnmatchedTripEpisodes_NeitherWindowKeepsDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	oldEp := tr.episode(t, tripEpisodesNow.Add(-40*24*time.Hour), tripEpisodesNow.Add(-30*24*time.Hour))
	router, _ := newTripEpisodeListRouter(t, pool)

	at := tripEpisodesNow.Add(-35 * 24 * time.Hour).Format(time.RFC3339)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?open_at=%s", tripEpisodesPath, at), oldEp)

	from := tripEpisodesNow.Add(-41 * 24 * time.Hour).Format(time.RFC3339)
	to := tripEpisodesNow.Add(-39 * 24 * time.Hour).Format(time.RFC3339)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", tripEpisodesPath, from, to), oldEp)
}

func TestListUnmatchedTripEpisodes_Filter_responses_and_static(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	ep1, openResp1, closeResp1 := tr.episodeWithResponses(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	ep2, _, _ := tr.episodeWithResponses(t, tripEpisodesNow.Add(-2*time.Hour), tripEpisodesNow.Add(-time.Hour))

	r1 := "route-alpha"
	r2 := "route-beta"
	v1, _ := tr.linkStaticWithRoute(t, ep1, tripEpisodesNow.Add(-10*time.Hour), true, &r1)
	v2, _ := tr.linkStaticWithRoute(t, ep2, tripEpisodesNow.Add(-5*time.Hour), true, &r2)

	router, _ := newTripEpisodeListRouter(t, pool)

	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?opening_response_id=%d", tripEpisodesPath, openResp1), ep1)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?closing_response_id=%d", tripEpisodesPath, closeResp1), ep1)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", tripEpisodesPath, v1), ep1)
	wantTripEpisodeIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", tripEpisodesPath, v2), ep2)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?route_id=route-alpha", ep1)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?route_id=route-beta", ep2)
}

func TestListUnmatchedTripEpisodes_Filter_recurrence(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	tr := newTripFixture(t, pool)
	ep1 := tr.episode(t, tripEpisodesNow.Add(-5*time.Hour), tripEpisodesNow.Add(-4*time.Hour))
	ep2 := tr.episode(t, tripEpisodesNow.Add(-3*time.Hour), tripEpisodesNow.Add(-2*time.Hour))
	router, _ := newTripEpisodeListRouter(t, pool)

	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?recurrence=has_previous_episode", ep2)
	wantTripEpisodeIDs(t, router, tripEpisodesPath+"?recurrence=no_previous_episode", ep1)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?recurrence=unknown", nil), "recurrence")
}

func TestListUnmatchedTripEpisodes_AgencyNone_Returns400(t *testing.T) {
	router, _ := newTripEpisodeListRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, tripEpisodesPath+"?agency_id=none", nil), "agency_id")
}
