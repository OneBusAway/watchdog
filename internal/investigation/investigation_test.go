package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
)

// newInvestigationRouter returns a router serving getInvestigationRoot for GET
// and HEAD, mounted without the request pipeline (see newServersRouter for why).
func newInvestigationRouter(t *testing.T) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	router := httprouter.New()
	router.GET("/v1/investigation", s.getInvestigationRoot)
	router.HEAD("/v1/investigation", s.getInvestigationRoot)
	return router
}

func TestGetInvestigationRoot_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/investigation"); h == nil {
			t.Errorf("%s /v1/investigation is not registered", method)
		}
	}
}

func TestGetInvestigationRoot_RejectsUnknownParameter(t *testing.T) {
	wantInvalidParam(t, doGet(t, newInvestigationRouter(t), "/v1/investigation?foo=1", nil), "foo")
}

func TestGetInvestigationRoot_Returns200WithLinks(t *testing.T) {
	rec := doGet(t, newInvestigationRouter(t), "/v1/investigation", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var body investigationRoot
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.APIVersion != "v1" {
		t.Errorf("api_version = %q, want \"v1\"", body.APIVersion)
	}
	wantLinks := []string{
		"self", "servers", "unmatched_stop_episodes", "unmatched_trip_episodes",
		"vehicle_anomaly_episodes", "reported_stops", "reported_trips", "vehicles",
		"gtfs_static_versions", "vehicle_anomaly_types", "cluster_schemes",
		"unmatched_stop_clusters", "episode_counts",
	}
	for _, k := range wantLinks {
		if _, ok := body.Links[k]; !ok {
			t.Errorf("links[%q] missing", k)
		}
	}
	if body.Links["self"] != "/v1/investigation" {
		t.Errorf("links.self = %q, want /v1/investigation", body.Links["self"])
	}
}

func TestGetInvestigationRoot_WorksWithoutDatabase(t *testing.T) {
	// E01 bypasses requireDatabase, so it must work even when Pool is nil.
	s, _ := newTestService(t)
	s.Pool = nil
	router := httprouter.New()
	router.GET("/v1/investigation", s.getInvestigationRoot)
	router.HEAD("/v1/investigation", s.getInvestigationRoot)
	rec := doGet(t, router, "/v1/investigation", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with no pool", rec.Code)
	}
}

func TestGetInvestigationRoot_Head(t *testing.T) {
	router := newInvestigationRouter(t)
	get := doGet(t, router, "/v1/investigation", nil)

	req := httptest.NewRequest(http.MethodHead, "/v1/investigation", nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
}
