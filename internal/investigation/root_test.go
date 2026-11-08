package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/julienschmidt/httprouter"
)

// newRootRouter returns a router serving getInvestigationRoot for GET and HEAD
// on a service with no database: the endpoint never reads one.
//
// The handler is mounted without the request pipeline: until #197 replaces
// the authenticate stub, every request through s.get is rejected with 401.
// The pipeline's own checks (401, 405, 406, 503) are tested in routes_test.go
// and pipeline_test.go.
func newRootRouter(t *testing.T) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	router := httprouter.New()
	router.GET("/v1/investigation", s.getInvestigationRoot)
	router.HEAD("/v1/investigation", s.getInvestigationRoot)
	return router
}

// wantRootBody is the body specified in the issue for E01.
const wantRootBody = `{
  "api_version": "v1",
  "links": {
    "self": "/v1/investigation",
    "servers": "/v1/servers",
    "unmatched_stop_episodes": "/v1/unmatched-stop-episodes",
    "unmatched_trip_episodes": "/v1/unmatched-trip-episodes",
    "vehicle_anomaly_episodes": "/v1/vehicle-anomaly-episodes",
    "reported_stops": "/v1/reported-stops",
    "reported_trips": "/v1/reported-trips",
    "vehicles": "/v1/vehicles",
    "gtfs_static_versions": "/v1/gtfs-static-versions",
    "vehicle_anomaly_types": "/v1/vehicle-anomaly-types",
    "cluster_schemes": "/v1/cluster-schemes",
    "unmatched_stop_clusters": "/v1/unmatched-stop-clusters",
    "episode_counts": "/v1/episode-counts"
  }
}`

func TestGetInvestigationRoot_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/investigation"); h == nil {
			t.Errorf("%s /v1/investigation is not registered", method)
		}
	}
}

func TestGetInvestigationRoot_Body(t *testing.T) {
	rec := doGet(t, newRootRouter(t), "/v1/investigation", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag header")
	}

	// Compared as JSON values: the encoder writes link names in sorted order,
	// not in the order the issue lists them.
	var got, want any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if err := json.Unmarshal([]byte(wantRootBody), &want); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body =\n%s\nwant\n%s", rec.Body, wantRootBody)
	}
}

func TestGetInvestigationRoot_RejectsUnknownParameter(t *testing.T) {
	router := newRootRouter(t)
	for _, query := range []string{"foo=1", "include=servers", "page_size=10"} {
		rec := doGet(t, router, "/v1/investigation?"+query, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("?%s: status = %d, want 400", query, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
			t.Errorf("?%s: Content-Type = %q, want application/problem+json", query, got)
		}
	}

	var body problem
	if err := json.NewDecoder(doGet(t, router, "/v1/investigation?foo=1", nil).Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.InvalidParams) != 1 || body.InvalidParams[0].Name != "foo" {
		t.Errorf("invalid_params = %+v, want one entry naming foo", body.InvalidParams)
	}
}

func TestGetInvestigationRoot_ETag(t *testing.T) {
	router := newRootRouter(t)
	first := doGet(t, router, "/v1/investigation", nil)
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, "/v1/investigation", map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetInvestigationRoot_Head(t *testing.T) {
	router := newRootRouter(t)
	get := doGet(t, router, "/v1/investigation", nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/v1/investigation", nil))

	if head.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", head.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
}
