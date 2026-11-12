package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// newVehicleAnomalyRuleVersionsRouter returns a router serving
// getVehicleAnomalyRuleVersion for GET and HEAD, mounted without the request
// pipeline (see newServersRouter for why).
func newVehicleAnomalyRuleVersionsRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool

	router := httprouter.New()
	router.GET("/v1/vehicle-anomaly-rule-versions/:rule_version_id", s.getVehicleAnomalyRuleVersion)
	router.HEAD("/v1/vehicle-anomaly-rule-versions/:rule_version_id", s.getVehicleAnomalyRuleVersion)
	return router
}

func TestGetVehicleAnomalyRuleVersion_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/vehicle-anomaly-rule-versions/:rule_version_id"); h == nil {
			t.Errorf("%s /v1/vehicle-anomaly-rule-versions/:rule_version_id is not registered", method)
		}
	}
}

func TestGetVehicleAnomalyRuleVersion_RejectsUnknownParameter(t *testing.T) {
	wantInvalidParam(t, doGet(t, newVehicleAnomalyRuleVersionsRouter(t, nil), "/v1/vehicle-anomaly-rule-versions/1?foo=bar", nil), "foo")
}

func TestGetVehicleAnomalyRuleVersion_MalformedIDReturns404(t *testing.T) {
	router := newVehicleAnomalyRuleVersionsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/vehicle-anomaly-rule-versions/"+id, nil))
		})
	}
}

func TestGetVehicleAnomalyRuleVersion_UnknownID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	wantNotFound(t, doGet(t, newVehicleAnomalyRuleVersionsRouter(t, pool), "/v1/vehicle-anomaly-rule-versions/999999", nil))
}

func TestGetVehicleAnomalyRuleVersion_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	effectiveAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// vehicle_anomaly_type rows are pre-seeded by the migration; use anomaly_type_id=1.
	if err := dbtest.Exec(pool,
		"INSERT INTO vehicle_anomaly_rule_version (rule_version_id, anomaly_type_id, rule_logic_version, version_name, effective_at) OVERRIDING SYSTEM VALUE VALUES (77, 1, 1, 'v1.0', $1)",
		effectiveAt); err != nil {
		t.Fatalf("insert rule_version: %v", err)
	}

	rec := doGet(t, newVehicleAnomalyRuleVersionsRouter(t, pool), "/v1/vehicle-anomaly-rule-versions/77", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		RuleVersionID int64             `json:"rule_version_id"`
		AnomalyCode   string            `json:"anomaly_code"`
		EffectiveAt   string            `json:"effective_at"`
		Links         map[string]string `json:"links"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RuleVersionID != 77 {
		t.Errorf("rule_version_id = %d, want 77", body.RuleVersionID)
	}
	if body.AnomalyCode == "" {
		t.Error("anomaly_code is empty; expected JOIN to populate it")
	}
	if body.EffectiveAt != "2026-01-01T00:00:00Z" {
		t.Errorf("effective_at = %q, want 2026-01-01T00:00:00Z", body.EffectiveAt)
	}
	if body.Links["self"] != "/v1/vehicle-anomaly-rule-versions/77" {
		t.Errorf("links.self = %q", body.Links["self"])
	}
}

func TestGetVehicleAnomalyRuleVersion_CacheImmutable(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	effectiveAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO vehicle_anomaly_rule_version (rule_version_id, anomaly_type_id, rule_logic_version, version_name, effective_at) OVERRIDING SYSTEM VALUE VALUES (77, 1, 1, 'v1.0', $1)",
		effectiveAt); err != nil {
		t.Fatalf("insert rule_version: %v", err)
	}

	rec := doGet(t, newVehicleAnomalyRuleVersionsRouter(t, pool), "/v1/vehicle-anomaly-rule-versions/77", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	cc := rec.Header().Get("Cache-Control")
	if cc != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want \"private, max-age=86400\"", cc)
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Error("Last-Modified header is missing")
	}
}

func TestGetVehicleAnomalyRuleVersion_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	effectiveAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO vehicle_anomaly_rule_version (rule_version_id, anomaly_type_id, rule_logic_version, version_name, effective_at) OVERRIDING SYSTEM VALUE VALUES (77, 1, 1, 'v1.0', $1)",
		effectiveAt); err != nil {
		t.Fatalf("insert rule_version: %v", err)
	}

	path := "/v1/vehicle-anomaly-rule-versions/77"
	router := newVehicleAnomalyRuleVersionsRouter(t, pool)
	get := doGet(t, router, path, nil)

	req := httptest.NewRequest(http.MethodHead, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, req)

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
}
