package investigation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
