package investigation

import (
	"context"
	"encoding/json"
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

// newOBAMetricsRouter mounts E20 with every investigation route's loaders, so include=server works.
func newOBAMetricsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET("/v1/oba-metrics-responses/:response_id", s.getOBAMetricsResponse)
	router.HEAD("/v1/oba-metrics-responses/:response_id", s.getOBAMetricsResponse)
	return router
}

// insertMetricsResponse records one OBA metrics response for serverID.
func insertMetricsResponse(t *testing.T, pool *pgxpool.Pool, serverID int64, observed time.Time, upstream *time.Time, version *int32) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		`INSERT INTO onebusaway_metrics_response (server_id, observed_at, upstream_current_time, api_version)
		 VALUES ($1, $2, $3, $4) RETURNING response_id`,
		serverID, observed, upstream, version)
}

// insertResponseAgency records one agency's counts for a response; nil counts are stored as NULL.
func insertResponseAgency(t *testing.T, pool *pgxpool.Pool, responseID, agencyID int64, complete bool, counts [4]*int32) {
	t.Helper()
	if err := dbtest.Exec(pool,
		`INSERT INTO onebusaway_metrics_response_agency
		 (response_id, agency_id, response_complete, unmatched_stop_count, unmatched_trip_count, matched_stop_count, matched_trip_count)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		responseID, agencyID, complete, counts[0], counts[1], counts[2], counts[3]); err != nil {
		t.Fatalf("insert response agency: %v", err)
	}
}

func int32Ptr(v int32) *int32 { return &v }

func TestGetOBAMetricsResponse_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyA := insertEpisodeAgency(t, pool, serverID, "1")
	agencyB := insertEpisodeAgency(t, pool, serverID, "2")
	observed := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	upstream := time.Date(2026, 10, 1, 1, 59, 58, 0, time.UTC)
	id := insertMetricsResponse(t, pool, serverID, observed, &upstream, int32Ptr(2))
	insertResponseAgency(t, pool, id, agencyA, true, [4]*int32{int32Ptr(3), int32Ptr(5), int32Ptr(40), int32Ptr(60)})
	insertResponseAgency(t, pool, id, agencyB, false, [4]*int32{nil, nil, nil, nil})
	router := newOBAMetricsRouter(t, pool)

	self := fmt.Sprintf("/v1/oba-metrics-responses/%d", id)
	want := map[string]any{
		"response_id":           float64(id),
		"server_id":             float64(serverID),
		"observed_at":           "2026-10-01T02:00:00Z",
		"upstream_current_time": "2026-10-01T01:59:58Z",
		"api_version":           float64(2),
		"agencies": []any{
			map[string]any{
				"agency_id": float64(agencyA), "response_complete": true,
				"unmatched_stop_count": float64(3), "unmatched_trip_count": float64(5),
				"matched_stop_count": float64(40), "matched_trip_count": float64(60),
			},
			map[string]any{
				"agency_id": float64(agencyB), "response_complete": false,
				"unmatched_stop_count": nil, "unmatched_trip_count": nil,
				"matched_stop_count": nil, "matched_trip_count": nil,
			},
		},
		"links": map[string]any{
			"self":   self,
			"server": fmt.Sprintf("/v1/servers/%d", serverID),
		},
	}
	if got := decodeEpisodeBody(t, doGet(t, router, self, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%v\nwant\n%v", got, want)
	}
}

func TestGetOBAMetricsResponse_UnknownIDReturns404(t *testing.T) {
	router := newOBAMetricsRouter(t, dbtest.NewMigratedPool(t))
	wantNotFound(t, doGet(t, router, "/v1/oba-metrics-responses/999", nil))
}

func TestGetOBAMetricsResponse_MalformedIDReturns404(t *testing.T) {
	router := newOBAMetricsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/oba-metrics-responses/"+id, nil))
		})
	}
}

func TestGetOBAMetricsResponse_RejectsUnknownParameter(t *testing.T) {
	router := newOBAMetricsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/oba-metrics-responses/1?foo=1", nil), "foo")
}

func TestGetOBAMetricsResponse_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	id := insertMetricsResponse(t, pool, serverID, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	router := newOBAMetricsRouter(t, pool)
	path := fmt.Sprintf("/v1/oba-metrics-responses/%d", id)

	first := doGet(t, router, path, nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	second := doGet(t, router, path, map[string]string{"If-None-Match": etag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetOBAMetricsResponse_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	id := insertMetricsResponse(t, pool, serverID, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	router := newOBAMetricsRouter(t, pool)
	path := fmt.Sprintf("/v1/oba-metrics-responses/%d", id)

	get := doGet(t, router, path, nil)
	head := headRequest(t, router, path)
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

func TestGetOBAMetricsResponse_AgenciesOrdered(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	first := insertEpisodeAgency(t, pool, serverID, "1")
	second := insertEpisodeAgency(t, pool, serverID, "2")
	id := insertMetricsResponse(t, pool, serverID, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), nil, nil)
	// Insert the higher agency first so ordering comes from the query, not insertion.
	insertResponseAgency(t, pool, id, second, true, [4]*int32{nil, nil, nil, nil})
	insertResponseAgency(t, pool, id, first, true, [4]*int32{nil, nil, nil, nil})
	router := newOBAMetricsRouter(t, pool)

	agencies := decodeEpisodeBody(t, doGet(t, router, fmt.Sprintf("/v1/oba-metrics-responses/%d", id), nil))["agencies"].([]any)
	if len(agencies) != 2 ||
		agencies[0].(map[string]any)["agency_id"] != float64(first) ||
		agencies[1].(map[string]any)["agency_id"] != float64(second) {
		t.Errorf("agencies = %v, want agency %d then %d", agencies, first, second)
	}
}

func TestOBAMetricsResponseLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agency := insertEpisodeAgency(t, pool, serverID, "1")
	ids := make([]int64, 0, 3)
	for i := range 3 {
		id := insertMetricsResponse(t, pool, serverID, episodesNow.Add(time.Duration(-i-1)*time.Hour), nil, nil)
		insertResponseAgency(t, pool, id, agency, true, [4]*int32{int32Ptr(int32(i)), nil, nil, nil})
		ids = append(ids, id)
	}
	// A response with no agency rows must still load, with an empty agencies array.
	ids = append(ids, insertMetricsResponse(t, pool, serverID, episodesNow.Add(-5*time.Hour), nil, nil))
	router := newOBAMetricsRouter(t, pool)

	s, _ := newTestService(t)
	s.Pool = pool
	var loaded map[int64]any
	err := s.read(context.Background(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		loaded, err = loadOBAMetricsResponses(ctx, q, ids)
		return err
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, id := range ids {
		want := decodeEpisodeBody(t, doGet(t, router, fmt.Sprintf("/v1/oba-metrics-responses/%d", id), nil))
		raw, err := json.Marshal(loaded[id])
		if err != nil {
			t.Fatalf("marshal loader value: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode loader value: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("response %d: loader value =\n%v\nwant endpoint body\n%v", id, got, want)
		}
	}
}
