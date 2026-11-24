package investigation

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

const vehicleAnomalyTypesPath = "/v1/vehicle-anomaly-types"

// newVehicleAnomalyTypesRouter mounts E30 for GET and HEAD on a service using pool.
func newVehicleAnomalyTypesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	router := httprouter.New()
	router.GET(vehicleAnomalyTypesPath, s.listVehicleAnomalyTypes)
	router.HEAD(vehicleAnomalyTypesPath, s.listVehicleAnomalyTypes)
	return router
}

func TestListVehicleAnomalyTypes_RejectsUnknownParameter(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, vehicleAnomalyTypesPath+"?foo=1", nil), "foo")
}

func TestListVehicleAnomalyTypes_Head(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	get := doGet(t, router, vehicleAnomalyTypesPath, nil)
	head := headRequest(t, router, vehicleAnomalyTypesPath)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", get.Code, http.StatusOK)
	}
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", head.Code, http.StatusOK)
	}
	for _, rec := range []struct {
		name string
		code int
		h    http.Header
	}{{"GET", get.Code, get.Header()}, {"HEAD", head.Code, head.Header()}} {
		if got := rec.h.Get("Cache-Control"); got != "private, max-age=30" {
			t.Errorf("%s Cache-Control = %q, want %q", rec.name, got, "private, max-age=30")
		}
		for _, h := range []string{"ETag", "Content-Type"} {
			if rec.h.Get(h) == "" {
				t.Errorf("%s %s is empty", rec.name, h)
			}
		}
	}
	for _, h := range []string{"Cache-Control", "ETag", "Content-Type"} {
		if head.Header().Get(h) != get.Header().Get(h) {
			t.Errorf("HEAD %s = %q, GET %s = %q", h, head.Header().Get(h), h, get.Header().Get(h))
		}
	}
	// net/http drops a HEAD body itself, so the body is not compared here.
}

// TestListVehicleAnomalyTypes_Seeded checks the three types the migration seeds,
// in anomaly_code order, with their links.
func TestListVehicleAnomalyTypes_Seeded(t *testing.T) {
	router := newVehicleAnomalyTypesRouter(t, dbtest.NewMigratedPool(t))
	body := decodeEpisodeBody(t, doGet(t, router, vehicleAnomalyTypesPath, nil))

	item := func(code, description string) map[string]any {
		return map[string]any{
			"anomaly_code": code,
			"description":  description,
			"links": map[string]any{
				"self":          vehicleAnomalyTypesPath + "/" + code,
				"rule_versions": vehicleAnomalyTypesPath + "/" + code + "/rule-versions",
			},
		}
	}
	want := map[string]any{
		"items": []any{
			item("invalid_coordinate", "The vehicle position has coordinates that fail validation."),
			item("stale_source_timestamp", "The vehicle's GTFS-Realtime source timestamp is older than the stale threshold."),
			item("stopped_out_of_bounds", "The vehicle is STOPPED_AT a location outside the applicable bounding box."),
		},
		"links": map[string]any{"self": vehicleAnomalyTypesPath},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body =\n%v\nwant\n%v", body, want)
	}
}
