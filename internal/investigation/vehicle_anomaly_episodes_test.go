package investigation

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newVehicleAnomalyEpisodesRouter mounts E24 and E25 at episodesNow with a fake loader for each kind.
func newVehicleAnomalyEpisodesRouter(t *testing.T, pool *pgxpool.Pool, kinds ...resourceKind) (http.Handler, fakeLoaders) {
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
	s.now = func() time.Time { return episodesNow }
	router := httprouter.New()
	router.GET("/v1/vehicle-anomaly-episodes", s.listVehicleAnomalyEpisodes)
	router.HEAD("/v1/vehicle-anomaly-episodes", s.listVehicleAnomalyEpisodes)
	router.GET("/v1/vehicle-anomaly-episodes/:episode_id", s.getVehicleAnomalyEpisode)
	router.HEAD("/v1/vehicle-anomaly-episodes/:episode_id", s.getVehicleAnomalyEpisode)
	return router, calls
}

// allVehicleAnomalyEpisodeKinds are the kinds behind every includable vehicle anomaly episode link.
var allVehicleAnomalyEpisodeKinds = []resourceKind{
	"vehicle", kindEndpoint, kindVehicleAnomalyRuleVersion, "vehicle_response",
	kindVehiclePositionEvidence, kindVehicleAnomalyEpisodeEvidence,
}

// newAnomalyVehicle records vehicle veh-1 on a new server's endpoint.
func newAnomalyVehicle(t *testing.T, pool *pgxpool.Pool) vehicleContext {
	t.Helper()
	return newVehicleContext(t, pool, insertVehicleServer(t, pool, "https://example.com"))
}

// openAnomalyEpisode records a stale_source_timestamp episode of vc opened at opened and still open.
func openAnomalyEpisode(t *testing.T, vc vehicleContext, opened time.Time) int64 {
	t.Helper()
	return vc.episode(t, "stale_source_timestamp", opened, time.Time{}, nil, nil)
}

// closedAnomalyEpisode records a stale_source_timestamp episode of vc closed by a fresh timestamp.
func closedAnomalyEpisode(t *testing.T, vc vehicleContext, opened, closed time.Time, agencyID *int64) int64 {
	t.Helper()
	reason := "fresh_source_timestamp"
	return vc.episode(t, "stale_source_timestamp", opened, closed, &reason, agencyID)
}

func vehicleAnomalyEpisodePath(id int64) string {
	return fmt.Sprintf("/v1/vehicle-anomaly-episodes/%d", id)
}

func TestVehicleAnomalyEpisode_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, path := range []string{"/v1/vehicle-anomaly-episodes", "/v1/vehicle-anomaly-episodes/1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if h, _, _ := router.Lookup(method, path); h == nil {
				t.Errorf("%s %s is not registered", method, path)
			}
		}
	}
}

func TestGetVehicleAnomalyEpisode_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "1")
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	closedAnomalyEpisode(t, vc, t0, t0.Add(time.Hour), nil)
	opened := t0.Add(2 * time.Hour)
	id := closedAnomalyEpisode(t, vc, opened, opened.Add(90*time.Second+500*time.Millisecond), &agencyID)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	var ruleVersionID, openingResponseID, closingResponseID, evidenceID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT e.rule_version_id, e.opening_response_id, e.closing_response_id, l.vehicle_position_evidence_id
		 FROM vehicle_anomaly_episode e
		 JOIN vehicle_anomaly_episode_evidence l
		   ON l.vehicle_anomaly_episode_id = e.vehicle_anomaly_episode_id AND l.evidence_role = 'opening'
		 WHERE e.vehicle_anomaly_episode_id = $1`, id).
		Scan(&ruleVersionID, &openingResponseID, &closingResponseID, &evidenceID); err != nil {
		t.Fatalf("read related IDs: %v", err)
	}
	self := vehicleAnomalyEpisodePath(id)
	want := map[string]any{
		"vehicle_anomaly_episode_id": float64(id),
		"status":                     "closed",
		"anomaly_type":               "stale_source_timestamp",
		"vehicle_id":                 "veh-1",
		"opened_at":                  "2026-10-01T02:00:00Z",
		"closed_at":                  "2026-10-01T02:01:30.5Z",
		"duration_seconds":           float64(90),
		"close_reason":               "fresh_source_timestamp",
		"previous_episode_count":     float64(1),
		"previous_closed_at":         "2026-10-01T01:00:00Z",
		"links": map[string]any{
			"self":             self,
			"vehicle":          fmt.Sprintf("/v1/vehicles/%d", vc.vehicleEntity),
			"endpoint":         fmt.Sprintf("/v1/vehicle-position-endpoints/%d", vc.endpointID),
			"rule_version":     fmt.Sprintf("/v1/vehicle-anomaly-rule-versions/%d", ruleVersionID),
			"opening_response": fmt.Sprintf("/v1/vehicle-position-responses/%d", openingResponseID),
			"closing_response": fmt.Sprintf("/v1/vehicle-position-responses/%d", closingResponseID),
			"opening_evidence": fmt.Sprintf("/v1/vehicle-position-evidence/%d", evidenceID),
			"evidence":         self + "/evidence",
		},
	}
	if got := decodeEpisodeBody(t, doGet(t, router, self, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%v\nwant\n%v", got, want)
	}
}

func TestGetVehicleAnomalyEpisode_OpenEpisodeRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	opened := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := openAnomalyEpisode(t, vc, opened)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	var ruleVersionID, openingResponseID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT rule_version_id, opening_response_id FROM vehicle_anomaly_episode WHERE vehicle_anomaly_episode_id = $1`, id).
		Scan(&ruleVersionID, &openingResponseID); err != nil {
		t.Fatalf("read related IDs: %v", err)
	}
	self := vehicleAnomalyEpisodePath(id)
	want := map[string]any{
		"vehicle_anomaly_episode_id": float64(id),
		"status":                     "open",
		"anomaly_type":               "stale_source_timestamp",
		"vehicle_id":                 "veh-1",
		"opened_at":                  "2026-10-01T00:00:00Z",
		"closed_at":                  nil,
		"duration_seconds":           nil,
		"close_reason":               nil,
		"previous_episode_count":     float64(0),
		"previous_closed_at":         nil,
		"links": map[string]any{
			"self":             self,
			"vehicle":          fmt.Sprintf("/v1/vehicles/%d", vc.vehicleEntity),
			"endpoint":         fmt.Sprintf("/v1/vehicle-position-endpoints/%d", vc.endpointID),
			"rule_version":     fmt.Sprintf("/v1/vehicle-anomaly-rule-versions/%d", ruleVersionID),
			"opening_response": fmt.Sprintf("/v1/vehicle-position-responses/%d", openingResponseID),
			"evidence":         self + "/evidence",
		},
	}
	if got := decodeEpisodeBody(t, doGet(t, router, self, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%v\nwant\n%v", got, want)
	}
}

func TestGetVehicleAnomalyEpisode_UnknownIDReturns404(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, vehicleAnomalyEpisodePath(999), nil))
}

func TestGetVehicleAnomalyEpisode_MalformedIDReturns404(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/vehicle-anomaly-episodes/"+id, nil))
		})
	}
}

func TestGetVehicleAnomalyEpisode_RejectsUnknownParameter(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodePath(1)+"?foo=1", nil), "foo")
}

func TestGetVehicleAnomalyEpisode_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := openAnomalyEpisode(t, newAnomalyVehicle(t, pool), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	first := doGet(t, router, vehicleAnomalyEpisodePath(id), nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, vehicleAnomalyEpisodePath(id), map[string]string{"If-None-Match": tag})
	wantStatus(t, second, http.StatusNotModified)
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetVehicleAnomalyEpisode_CachePolicy(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	closedAt := episodesNow.Add(-time.Hour)
	closed := closedAnomalyEpisode(t, vc, episodesNow.Add(-2*time.Hour), closedAt, nil)
	open := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Minute))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool, "vehicle")

	for _, tc := range []struct {
		path, cacheControl, lastModified string
	}{
		{vehicleAnomalyEpisodePath(closed), "private, max-age=86400", closedAt.Format(http.TimeFormat)},
		{vehicleAnomalyEpisodePath(closed) + "?include=vehicle", "private, max-age=30", ""},
		{vehicleAnomalyEpisodePath(open), "private, max-age=30", ""},
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

func TestGetVehicleAnomalyEpisode_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := openAnomalyEpisode(t, newAnomalyVehicle(t, pool), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantEpisodeHead(t, router, vehicleAnomalyEpisodePath(id))
}

func TestGetVehicleAnomalyEpisode_IncludeEach(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "1")
	id := closedAnomalyEpisode(t, vc, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), &agencyID)
	router, calls := newVehicleAnomalyEpisodesRouter(t, pool, allVehicleAnomalyEpisodeKinds...)

	names := []string{"vehicle", "endpoint", "rule_version", "opening_response", "closing_response", "opening_evidence", "evidence"}
	body := decodeEpisodeBody(t, doGet(t, router,
		vehicleAnomalyEpisodePath(id)+"?include=vehicle,endpoint,rule_version,opening_response,closing_response,opening_evidence,evidence", nil))
	got := body["included"].(map[string]any)
	l := body["links"].(map[string]any)
	if len(got) != len(names) {
		t.Errorf("included has %d entries, want %d: %v", len(got), len(names), got)
	}
	for _, name := range names {
		if _, ok := got[l[name].(string)]; !ok {
			t.Errorf("included has no entry for %s (%s)", name, l[name])
		}
	}
	if calls["vehicle_response"] != 1 {
		t.Errorf("response loader called %d times, want 1", calls["vehicle_response"])
	}
}

func TestGetVehicleAnomalyEpisode_IncludeAbsentTarget(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := openAnomalyEpisode(t, newAnomalyVehicle(t, pool), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool, allVehicleAnomalyEpisodeKinds...)

	body := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodePath(id)+"?include=closing_response,opening_evidence", nil))
	if got := body["included"].(map[string]any); len(got) != 0 {
		t.Errorf("included = %v, want empty", got)
	}
}

func TestGetVehicleAnomalyEpisode_IncludeRejectsUnknown(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil, allVehicleAnomalyEpisodeKinds...)
	for _, include := range []string{"foo", "vehicle,vehicle", ""} {
		t.Run(include, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodePath(1)+"?include="+include, nil), "include")
		})
	}
}

func TestGetVehicleAnomalyEpisode_NoIncludedKeyByDefault(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := openAnomalyEpisode(t, newAnomalyVehicle(t, pool), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool, allVehicleAnomalyEpisodeKinds...)

	if _, ok := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodePath(id), nil))["included"]; ok {
		t.Error("body has an included key")
	}
}

const vehicleAnomalyEpisodesPath = "/v1/vehicle-anomaly-episodes"

// vehicleAnomalyEpisodesWindowQuery is the E24 path with the active window [from, to) and extra parameters.
func vehicleAnomalyEpisodesWindowQuery(from, to time.Time, extra url.Values) string {
	q := url.Values{
		"active_from": {from.Format(time.RFC3339)},
		"active_to":   {to.Format(time.RFC3339)},
	}
	for name, v := range extra {
		q[name] = v
	}
	return vehicleAnomalyEpisodesPath + "?" + q.Encode()
}

// otherEndpointVehicle records vehicle veh-1 on a second endpoint of vc's server.
func otherEndpointVehicle(t *testing.T, vc vehicleContext) vehicleContext {
	t.Helper()
	vc.endpointID = insertVehicleEndpoint(t, vc.pool, vc.serverID, "https://vp2.example.com")
	vc.vehicleEntity = insertVehicle(t, vc.pool, vc.endpointID, "veh-1")
	return vc
}

func TestListVehicleAnomalyEpisodes_ReturnsNewestFirst(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	a := closedAnomalyEpisode(t, vc, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour), nil)
	b := closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil)
	c := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath, c, b, a)
}

func TestListVehicleAnomalyEpisodes_ItemMatchesDetail(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "1")
	closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), &agencyID)
	openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	items := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodesPath, nil))["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, item := range items {
		got := item.(map[string]any)
		id := int64(got["vehicle_anomaly_episode_id"].(float64))
		if want := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodePath(id), nil)); !reflect.DeepEqual(got, want) {
			t.Errorf("episode %d: item\n%v\ndetail\n%v", id, got, want)
		}
	}
}

func TestListVehicleAnomalyEpisodes_Paginates(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	a := closedAnomalyEpisode(t, vc, episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour), nil)
	b := closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil)
	c := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	first := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?page_size=2", nil))
	if got := vehicleEpisodeIDs(t, first); !reflect.DeepEqual(got, []int64{c, b}) {
		t.Errorf("first page = %v, want %v", got, []int64{c, b})
	}
	next, ok := first["links"].(map[string]any)["next"].(string)
	if !ok {
		t.Fatal("first page has no links.next")
	}
	second := decodeEpisodeBody(t, doGet(t, router, next, nil))
	if got := vehicleEpisodeIDs(t, second); !reflect.DeepEqual(got, []int64{a}) {
		t.Errorf("second page = %v, want %v", got, []int64{a})
	}
	if _, ok := second["links"].(map[string]any)["next"]; ok {
		t.Error("last page has links.next")
	}
}

func TestListVehicleAnomalyEpisodes_EmptyResult(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, dbtest.NewMigratedPool(t))
	rec := doGet(t, router, vehicleAnomalyEpisodesPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	body := decodeEpisodeBody(t, rec)
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", body["items"])
	}
	if _, ok := body["links"].(map[string]any)["next"]; ok {
		t.Error("empty result has links.next")
	}
}

func TestListVehicleAnomalyEpisodes_RejectsBadPageSize(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	for _, size := range []string{"0", "201"} {
		wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?page_size="+size, nil), "page_size")
	}
}

func TestListVehicleAnomalyEpisodes_RejectsBadCursor(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?cursor=%25%25%25", nil), "cursor")
}

func TestListVehicleAnomalyEpisodes_Filter_server_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	other := newVehicleContext(t, pool, insertVehicleServer(t, pool, "https://other.example.com"))
	id := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	otherID := openAnomalyEpisode(t, other, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?server_id=%d", vehicleAnomalyEpisodesPath, vc.serverID), id)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?server_id=%d", vehicleAnomalyEpisodesPath, other.serverID), otherID)
}

func TestListVehicleAnomalyEpisodes_Filter_endpoint_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	other := otherEndpointVehicle(t, vc)
	id := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	otherID := openAnomalyEpisode(t, other, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?endpoint_id=%d", vehicleAnomalyEpisodesPath, vc.endpointID), id)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?endpoint_id=%d", vehicleAnomalyEpisodesPath, other.endpointID), otherID)
}

// TestListVehicleAnomalyEpisodes_Filter_vehicle_id checks that vehicle_id
// matches the feed ID within its scope, here on two endpoints of one server.
func TestListVehicleAnomalyEpisodes_Filter_vehicle_id(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	sameIDElsewhere := otherEndpointVehicle(t, vc)
	id := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	elsewhereID := openAnomalyEpisode(t, sameIDElsewhere, episodesNow.Add(-time.Hour))
	openAnomalyEpisode(t, vc.withVehicle(t, "veh-2"), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router,
		fmt.Sprintf("%s?vehicle_id=veh-1&endpoint_id=%d", vehicleAnomalyEpisodesPath, vc.endpointID), id)
	wantVehicleEpisodeIDs(t, router,
		fmt.Sprintf("%s?vehicle_id=veh-1&server_id=%d", vehicleAnomalyEpisodesPath, vc.serverID), elsewhereID, id)
}

func TestListVehicleAnomalyEpisodes_Filter_anomaly_type(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	stale := openAnomalyEpisode(t, vc, episodesNow.Add(-2*time.Hour))
	invalid := vc.episode(t, "invalid_coordinate", episodesNow.Add(-time.Hour), time.Time{}, nil, nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?anomaly_type=stale_source_timestamp", stale)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?anomaly_type=invalid_coordinate", invalid)
}

func TestListVehicleAnomalyEpisodes_Filter_status(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	closed := closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil)
	open := openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?status=open", open)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?status=closed", closed)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?status=other", nil), "status")
}

func TestListVehicleAnomalyEpisodes_Filter_close_reason(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	fresh := closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil)
	gone := "not_present_in_full_dataset"
	notPresent := vc.episode(t, "stale_source_timestamp", episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), &gone, nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?close_reason=fresh_source_timestamp", fresh)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?close_reason=not_present_in_full_dataset", notPresent)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?close_reason=not_reported", nil), "close_reason")
}

func TestListVehicleAnomalyEpisodes_RejectsUnknownParameter(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?foo=1", nil), "foo")
}

func TestListVehicleAnomalyEpisodes_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	openAnomalyEpisode(t, newAnomalyVehicle(t, pool), episodesNow.Add(-time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantEpisodeHead(t, router, vehicleAnomalyEpisodesPath)
	get := doGet(t, router, vehicleAnomalyEpisodesPath, nil)
	if got := get.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if get.Header().Get("ETag") == "" {
		t.Error("no ETag header")
	}
}

func TestListVehicleAnomalyEpisodes_WindowOverlap(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	from, to := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	closedAnomalyEpisode(t, vc, from.Add(-3*time.Hour), from.Add(-2*time.Hour), nil)
	overlapping := closedAnomalyEpisode(t, vc, from.Add(-time.Hour), from.Add(time.Hour), nil)
	stillOpen := openAnomalyEpisode(t, vc, from.Add(-5*time.Hour))
	closedAnomalyEpisode(t, vc, to.Add(time.Hour), to.Add(2*time.Hour), nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesWindowQuery(from, to, nil), stillOpen, overlapping)
}

func TestListVehicleAnomalyEpisodes_WindowNeedsBothEnds(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?active_from=2026-10-01T00:00:00Z", nil), "active_to")
	same := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesWindowQuery(same, same, nil), nil), "active_to")
}

func TestListVehicleAnomalyEpisodes_WindowRequiresOffset(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router,
		vehicleAnomalyEpisodesPath+"?active_from=2026-10-01T00:00:00&active_to=2026-10-02T00:00:00Z", nil), "active_from")
}

func TestListVehicleAnomalyEpisodes_WindowDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	closedAnomalyEpisode(t, vc, episodesNow.Add(-26*time.Hour), episodesNow.Add(-25*time.Hour), nil)
	recent := closedAnomalyEpisode(t, vc, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath, recent)
}

func TestListVehicleAnomalyEpisodes_WindowMax31Days(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesWindowQuery(from, from.Add(32*24*time.Hour), nil), nil), "active_to")
}

func TestListVehicleAnomalyEpisodes_OpenStatusIgnoresDefaultWindow(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	closedAnomalyEpisode(t, vc, episodesNow.Add(-41*24*time.Hour), episodesNow.Add(-40*24*time.Hour), nil)
	id := openAnomalyEpisode(t, vc, episodesNow.Add(-40*24*time.Hour))
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?status=open", id)
}

func TestListVehicleAnomalyEpisodes_VehicleIDRequiresScope(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?vehicle_id=veh-1", nil), "vehicle_id")
}

// TestListVehicleAnomalyEpisodes_EmptyVehicleIDReportedOnce checks that an
// empty vehicle_id with no scope is one problem, not two for the same field.
func TestListVehicleAnomalyEpisodes_EmptyVehicleIDReportedOnce(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?vehicle_id=", nil), "vehicle_id")
}

func TestListVehicleAnomalyEpisodes_AgencyFromOpeningEvidence(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "3")
	attributed := closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), &agencyID)
	closedAnomalyEpisode(t, vc, episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?agency_id=%d", vehicleAnomalyEpisodesPath, agencyID), attributed)
}

func TestListVehicleAnomalyEpisodes_BadType(t *testing.T) {
	router, _ := newVehicleAnomalyEpisodesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?anomaly_type=speed", nil), "anomaly_type")
}

func TestListVehicleAnomalyEpisodes_IncludeOpeningEvidence(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "1")
	closedAnomalyEpisode(t, vc, episodesNow.Add(-4*time.Hour), episodesNow.Add(-3*time.Hour), &agencyID)
	closedAnomalyEpisode(t, vc, episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), &agencyID)
	openAnomalyEpisode(t, vc, episodesNow.Add(-time.Hour))
	router, calls := newVehicleAnomalyEpisodesRouter(t, pool, kindVehiclePositionEvidence)

	body := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?include=opening_evidence", nil))
	want := map[string]bool{}
	for _, item := range body["items"].([]any) {
		if u, ok := item.(map[string]any)["links"].(map[string]any)["opening_evidence"].(string); ok {
			want[u] = true
		}
	}
	if len(want) != 2 {
		t.Fatalf("%d items link opening evidence, want 2", len(want))
	}
	got := body["included"].(map[string]any)
	if len(got) != len(want) {
		t.Errorf("included has %d entries, want %d: %v", len(got), len(want), got)
	}
	for u := range want {
		if _, ok := got[u]; !ok {
			t.Errorf("included has no entry for %s", u)
		}
	}
	if calls[kindVehiclePositionEvidence] != 1 {
		t.Errorf("opening_evidence loader called %d times, want 1", calls[kindVehiclePositionEvidence])
	}
}

type evidenceOptions struct {
	agencyID                 *int64
	routeID                  *string
	gtfsStaticVersionID      *int64
	boundingBoxVersionID     *int64
	positionValidationReason *string
	attributionReason        *string
	expectedScheduleState    *string
}

func (vc vehicleContext) episodeWithEvidence(
	t *testing.T,
	anomalyCode string,
	opened, closed time.Time,
	closeReason *string,
	ruleVersionID *int64,
	opts *evidenceOptions,
) (epID, openRespID, closeRespID int64) {
	t.Helper()
	var typeID int16
	if err := vc.pool.QueryRow(context.Background(),
		"SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = $1", anomalyCode).Scan(&typeID); err != nil {
		t.Fatalf("query anomaly_type_id: %v", err)
	}

	rID := int64(0)
	if ruleVersionID != nil {
		rID = *ruleVersionID
	} else {
		err := vc.pool.QueryRow(context.Background(),
			"SELECT rule_version_id FROM vehicle_anomaly_rule_version WHERE anomaly_type_id = $1 LIMIT 1", typeID).Scan(&rID)
		if err != nil {
			rID = dbtest.ID(t, vc.pool,
				"INSERT INTO vehicle_anomaly_rule_version (anomaly_type_id, rule_logic_version, version_name, effective_at) VALUES ($1, 1, 'v1', now()) RETURNING rule_version_id",
				typeID)
		}
	}

	openRespID = dbtest.ID(t, vc.pool,
		"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
		vc.endpointID, opened)

	var closingResponseID *int64
	if !closed.IsZero() {
		id := dbtest.ID(t, vc.pool,
			"INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality) VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id",
			vc.endpointID, closed)
		closingResponseID = &id
		closeRespID = id
		if closeReason == nil {
			defaultReason := "fresh_source_timestamp"
			closeReason = &defaultReason
		}
	}

	epID = dbtest.ID(t, vc.pool,
		`INSERT INTO vehicle_anomaly_episode
		 (vehicle_entity_id, anomaly_type_id, rule_version_id, opening_response_id, closing_response_id, close_reason)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING vehicle_anomaly_episode_id`,
		vc.vehicleEntity, typeID, rID, openRespID, closingResponseID, closeReason)

	if opts != nil {
		posValid := opts.positionValidationReason == nil
		var schedReason *string
		if opts.expectedScheduleState != nil {
			switch *opts.expectedScheduleState {
			case "expected_active":
				r := "within_scheduled_window"
				schedReason = &r
			case "expected_inactive":
				r := "before_scheduled_window"
				schedReason = &r
			case "unknown":
				r := "no_trip_descriptor"
				schedReason = &r
			}
		}
		evidenceID := dbtest.ID(t, vc.pool,
			`INSERT INTO vehicle_position_evidence
			 (vehicle_position_response_id, vehicle_entity_id, agency_id, position_valid,
			  position_validation_reason, attribution_reason, expected_schedule_state, expected_schedule_reason,
			  route_id, gtfs_static_version_id, bounding_box_version_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING vehicle_position_evidence_id`,
			openRespID, vc.vehicleEntity, opts.agencyID, posValid,
			opts.positionValidationReason, opts.attributionReason, opts.expectedScheduleState, schedReason,
			opts.routeID, opts.gtfsStaticVersionID, opts.boundingBoxVersionID)
		if err := dbtest.Exec(vc.pool,
			`INSERT INTO vehicle_anomaly_episode_evidence
			 (vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role)
			 VALUES ($1, $2, 'opening')`,
			epID, evidenceID); err != nil {
			t.Fatalf("insert episode evidence: %v", err)
		}
	}
	return epID, openRespID, closeRespID
}

func TestListVehicleAnomalyEpisodes_Filter_opened_window(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	vc.episode(t, "stale_source_timestamp", episodesNow.Add(-10*time.Hour), time.Time{}, nil, nil)
	inWindow := vc.episode(t, "invalid_coordinate", episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	from := episodesNow.Add(-5 * time.Hour).Format(time.RFC3339)
	to := episodesNow.Add(-time.Hour).Format(time.RFC3339)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", vehicleAnomalyEpisodesPath, from, to), inWindow)

	// Window pair rules
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s", vehicleAnomalyEpisodesPath, from), nil), "opened_to")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_to=%s", vehicleAnomalyEpisodesPath, to), nil), "opened_from")
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", vehicleAnomalyEpisodesPath, to, from), nil), "opened_to")
	over31 := episodesNow.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", vehicleAnomalyEpisodesPath, over31, to), nil), "opened_to")
}

func TestListVehicleAnomalyEpisodes_Filter_open_at(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	tInstant := episodesNow.Add(-10 * time.Hour)

	vc.episode(t, "stale_source_timestamp", tInstant.Add(-3*time.Hour), tInstant.Add(-time.Hour), nil, nil)
	openAcross := vc.episode(t, "invalid_coordinate", tInstant.Add(-time.Hour), tInstant.Add(time.Hour), nil, nil)
	vc.episode(t, "stopped_out_of_bounds", tInstant.Add(time.Hour), tInstant.Add(3*time.Hour), nil, nil)

	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?open_at=%s", vehicleAnomalyEpisodesPath, tInstant.Format(time.RFC3339)), openAcross)

	// Future open_at returns 400
	future := episodesNow.Add(time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, fmt.Sprintf("%s?open_at=%s", vehicleAnomalyEpisodesPath, future), nil), "open_at")
}

func TestListVehicleAnomalyEpisodes_NeitherWindowKeepsDefault24h(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	oldEp := vc.episode(t, "stale_source_timestamp", episodesNow.Add(-40*24*time.Hour), episodesNow.Add(-30*24*time.Hour), nil, nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	at := episodesNow.Add(-35 * 24 * time.Hour).Format(time.RFC3339)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?open_at=%s", vehicleAnomalyEpisodesPath, at), oldEp)

	from := episodesNow.Add(-41 * 24 * time.Hour).Format(time.RFC3339)
	to := episodesNow.Add(-39 * 24 * time.Hour).Format(time.RFC3339)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?opened_from=%s&opened_to=%s", vehicleAnomalyEpisodesPath, from, to), oldEp)
}

func TestListVehicleAnomalyEpisodes_Filter_responses_and_static(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)

	v1 := dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		vc.serverID, episodesNow.Add(-10*time.Hour))
	v2 := dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		vc.serverID, episodesNow.Add(-5*time.Hour))

	ep1, openResp1, closeResp1 := vc.episodeWithEvidence(t, "stale_source_timestamp",
		episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, nil,
		&evidenceOptions{gtfsStaticVersionID: &v1})

	vc2 := vc.withVehicle(t, "veh-2")
	ep2, _, _ := vc2.episodeWithEvidence(t, "invalid_coordinate",
		episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil, nil,
		&evidenceOptions{gtfsStaticVersionID: &v2})

	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?opening_response_id=%d", vehicleAnomalyEpisodesPath, openResp1), ep1)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?closing_response_id=%d", vehicleAnomalyEpisodesPath, closeResp1), ep1)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", vehicleAnomalyEpisodesPath, v1), ep1)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?gtfs_static_version_id=%d", vehicleAnomalyEpisodesPath, v2), ep2)
}

func TestListVehicleAnomalyEpisodes_Filter_recurrence(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	ep1 := vc.episode(t, "stale_source_timestamp", episodesNow.Add(-5*time.Hour), episodesNow.Add(-4*time.Hour), nil, nil)
	ep2 := vc.episode(t, "stale_source_timestamp", episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, nil)
	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?recurrence=has_previous_episode", ep2)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?recurrence=no_previous_episode", ep1)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?recurrence=unknown", nil), "recurrence")
}

func TestListVehicleAnomalyEpisodes_Filter_route_and_versions(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)

	vStatic := dbtest.ID(t, pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		vc.serverID, episodesNow.Add(-10*time.Hour))
	boxID := dbtest.ID(t, pool,
		`INSERT INTO bounding_box_version (gtfs_static_version_id, server_id, minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude)
		 VALUES ($1, $2, 47.0, 48.0, -123.0, -122.0) RETURNING bounding_box_version_id`,
		vStatic, vc.serverID)

	ruleID := dbtest.ID(t, pool,
		`INSERT INTO vehicle_anomaly_rule_version (anomaly_type_id, rule_logic_version, version_name, effective_at)
		 VALUES (1, 2, 'v2-test', $1) RETURNING rule_version_id`,
		episodesNow.Add(-20*time.Hour))

	r1 := "route-99"
	ep1, _, _ := vc.episodeWithEvidence(t, "stale_source_timestamp",
		episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, &ruleID,
		&evidenceOptions{routeID: &r1, boundingBoxVersionID: &boxID})

	vc2 := vc.withVehicle(t, "veh-other")
	ep2, _, _ := vc2.episodeWithEvidence(t, "invalid_coordinate",
		episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil, nil, nil)

	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?route_id=route-99", ep1)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?rule_version_id=%d", vehicleAnomalyEpisodesPath, ruleID), ep1)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?bounding_box_version_id=%d", vehicleAnomalyEpisodesPath, boxID), ep1)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?route_id=route-unknown")
	_ = ep2
}

func TestListVehicleAnomalyEpisodes_Filter_evidence_enums(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)

	valReason := "placeholder_zero_coordinate"
	attrReason := "unknown_route_id"
	schedState := "expected_active"

	ep1, _, _ := vc.episodeWithEvidence(t, "invalid_coordinate",
		episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, nil,
		&evidenceOptions{
			positionValidationReason: &valReason,
			attributionReason:        &attrReason,
			expectedScheduleState:    &schedState,
		})

	vc2 := vc.withVehicle(t, "veh-other")
	ep2, _, _ := vc2.episodeWithEvidence(t, "stale_source_timestamp",
		episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil, nil, nil)

	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?position_validation_reason=placeholder_zero_coordinate", ep1)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?attribution_reason=unknown_route_id", ep1)
	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?expected_schedule_state=expected_active", ep1)

	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?position_validation_reason=bad", nil), "position_validation_reason")
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?attribution_reason=bad", nil), "attribution_reason")
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyEpisodesPath+"?expected_schedule_state=bad", nil), "expected_schedule_state")
	_ = ep2
}

func TestListVehicleAnomalyEpisodes_Filter_agency_none(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	vc := newAnomalyVehicle(t, pool)
	agencyID := insertEpisodeAgency(t, pool, vc.serverID, "7")

	attributed, _, _ := vc.episodeWithEvidence(t, "stale_source_timestamp",
		episodesNow.Add(-3*time.Hour), episodesNow.Add(-2*time.Hour), nil, nil,
		&evidenceOptions{agencyID: &agencyID})

	vc2 := vc.withVehicle(t, "veh-unattr")
	unattributed, _, _ := vc2.episodeWithEvidence(t, "stale_source_timestamp",
		episodesNow.Add(-2*time.Hour), episodesNow.Add(-time.Hour), nil, nil,
		&evidenceOptions{agencyID: nil})

	router, _ := newVehicleAnomalyEpisodesRouter(t, pool)

	wantVehicleEpisodeIDs(t, router, vehicleAnomalyEpisodesPath+"?agency_id=none", unattributed)
	wantVehicleEpisodeIDs(t, router, fmt.Sprintf("%s?agency_id=%d", vehicleAnomalyEpisodesPath, agencyID), attributed)
}
