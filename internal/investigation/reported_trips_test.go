package investigation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

// Mount the handler directly while #197's authentication still fails closed.
// Registration through the production pipeline is checked separately.
func newReportedTripsRouter(t *testing.T, pool *pgxpool.Pool) (*InvestigationService, http.Handler) {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	r := httprouter.New()
	r.GET("/v1/reported-trips", s.listReportedTrips)
	r.HEAD("/v1/reported-trips", s.listReportedTrips)
	return s, r
}

func TestReportedTrips_Registered(t *testing.T) {
	_, r := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := r.Lookup(method, "/v1/reported-trips"); h == nil {
			t.Errorf("%s route missing", method)
		}
	}
}

func TestListReportedTrips_RejectsUnknownParameter(t *testing.T) {
	_, r := newReportedTripsRouter(t, nil)
	wantInvalidParam(t, doGet(t, r, "/v1/reported-trips?agency_id=1&trip_id=T&foo=1", nil), "foo")
}

func TestListReportedTrips_RequiresBoth(t *testing.T) {
	_, r := newReportedTripsRouter(t, nil)
	for _, tc := range []struct{ query, name string }{
		{"trip_id=T", "agency_id"}, {"agency_id=1", "trip_id"},
		{"agency_id=&trip_id=T", "agency_id"}, {"agency_id=0&trip_id=T", "agency_id"},
		{"agency_id=-1&trip_id=T", "agency_id"}, {"agency_id=abc&trip_id=T", "agency_id"},
		{"agency_id=01&trip_id=T", "agency_id"}, {"agency_id=1&trip_id=", "trip_id"},
		{"agency_id=1&agency_id=2&trip_id=T", "agency_id"}, {"agency_id=1&trip_id=T&trip_id=U", "trip_id"},
	} {
		t.Run(tc.query, func(t *testing.T) { wantInvalidParam(t, doGet(t, r, "/v1/reported-trips?"+tc.query, nil), tc.name) })
	}
	if rec := doGet(t, r, "/v1/reported-trips", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing both: %d", rec.Code)
	}
}

func TestListReportedTrips_RejectsIncludeWithoutLoader(t *testing.T) {
	s, r := newReportedTripsRouter(t, nil)
	delete(s.loaders, "agency")
	wantInvalidParam(t, doGet(t, r, "/v1/reported-trips?agency_id=1&trip_id=T&include=agency", nil), "include")
}

func reportedTripFixture(t *testing.T) (*pgxpool.Pool, int64, int64) {
	t.Helper()
	pool := dbtest.NewMigratedPool(t)
	server := dbtest.ID(t, pool, "INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('https://example.com', 'Example') RETURNING server_id")
	agency := dbtest.ID(t, pool, "INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'A') RETURNING agency_id", server)
	other := dbtest.ID(t, pool, "INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'B') RETURNING agency_id", server)
	id := dbtest.ID(t, pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'Trip A') RETURNING reported_trip_id", agency)
	if err := dbtest.Exec(pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'Trip A'), ($2, 'trip a'), ($2, 'Other')", other, agency); err != nil {
		t.Fatal(err)
	}
	return pool, agency, id
}

func TestListReportedTrips_Lookup(t *testing.T) {
	pool, agency, id := reportedTripFixture(t)
	_, r := newReportedTripsRouter(t, pool)
	path := "/v1/reported-trips?agency_id=" + strconv.FormatInt(agency, 10) + "&trip_id=Trip%20A"
	rec := doGet(t, r, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	self := "/v1/reported-trips/" + strconv.FormatInt(id, 10)
	want := reportedTripsBody{Items: []reportedTrip{{ReportedTripID: id, AgencyID: agency, TripID: "Trip A", Links: links{
		"self": self, "agency": "/v1/agencies/" + strconv.FormatInt(agency, 10), "unmatched_trip_episodes": self + "/unmatched-trip-episodes",
	}}}, Links: links{"self": "/v1/reported-trips?agency_id=" + strconv.FormatInt(agency, 10) + "&trip_id=Trip+A"}}
	expected, err := encodeJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(expected) {
		t.Errorf("body %s, want %s", rec.Body, expected)
	}
	for _, query := range []string{"agency_id=" + strconv.FormatInt(agency, 10) + "&trip_id=missing", "agency_id=999999&trip_id=Trip%20A"} {
		empty := doGet(t, r, "/v1/reported-trips?"+query, nil)
		if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
			t.Errorf("no match: %d %s", empty.Code, empty.Body)
		}
	}
}

func TestListReportedTrips_Head(t *testing.T) {
	pool, agency, _ := reportedTripFixture(t)
	_, r := newReportedTripsRouter(t, pool)
	server := httptest.NewServer(r)
	defer server.Close()
	url := server.URL + "/v1/reported-trips?agency_id=" + strconv.FormatInt(agency, 10) + "&trip_id=Trip%20A"
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
			t.Errorf("%s differs", key)
		}
	}
	data, err := io.ReadAll(head.Body)
	if err != nil || len(data) != 0 {
		t.Errorf("HEAD body %q, error %v", data, err)
	}
	if get.Header.Get("Cache-Control") != "private, max-age=30" || get.Header.Get("ETag") == "" {
		t.Errorf("cache headers %v", get.Header)
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-None-Match", get.Header.Get("ETag"))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err = io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusNotModified || len(data) != 0 {
		t.Errorf("conditional GET: %d %q %v", response.StatusCode, data, err)
	}
}

func TestListReportedTrips_IncludeAgency(t *testing.T) {
	pool, agency, _ := reportedTripFixture(t)
	s, r := newReportedTripsRouter(t, pool)
	delete(s.loaders, "agency")
	calls := 0
	rep := map[string]any{"agency_id": agency}
	s.registerLoader("agency", func(_ context.Context, _ *dbgen.Queries, ids []int64) (map[int64]any, error) {
		calls++
		if !reflect.DeepEqual(ids, []int64{agency}) {
			t.Errorf("loader IDs %v", ids)
		}
		return map[int64]any{agency: rep}, nil
	})
	path := "/v1/reported-trips?agency_id=" + strconv.FormatInt(agency, 10) + "&trip_id=Trip%20A&include=agency"
	rec := doGet(t, r, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body reportedTripsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want, _ := encodeJSON(included{"/v1/agencies/" + strconv.FormatInt(agency, 10): rep})
	got, _ := encodeJSON(body.Included)
	if calls != 1 || string(got) != string(want) {
		t.Errorf("calls %d included %s want %s", calls, got, want)
	}
	empty := doGet(t, r, strings.Replace(path, "Trip%20A", "missing", 1), nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"included":{}`) || calls != 1 {
		t.Errorf("empty include: %d %s calls %d", empty.Code, empty.Body, calls)
	}
}
