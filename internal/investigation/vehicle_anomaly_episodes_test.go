package investigation

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newVehicleAnomalyEpisodesRouter mounts E25 with a fake loader for each kind.
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
	router := httprouter.New()
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
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-anomaly-episodes/1"); h == nil {
			t.Errorf("%s /v1/vehicle-anomaly-episodes/1 is not registered", method)
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
