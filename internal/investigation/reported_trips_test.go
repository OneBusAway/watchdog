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

// newReportedTripDetailRouter mounts E12 on a bare router, skipping the
// pipeline's fail-closed authenticate; registration is covered by
// TestReportedTrips_Registered.
func newReportedTripDetailRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	r := httprouter.New()
	r.GET("/v1/reported-trips/:reported_trip_id", s.getReportedTrip)
	r.HEAD("/v1/reported-trips/:reported_trip_id", s.getReportedTrip)
	return r
}

func reportedTripPath(id int64) string {
	return "/v1/reported-trips/" + strconv.FormatInt(id, 10)
}

func TestReportedTrips_Registered(t *testing.T) {
	s, r := newTestService(t)
	for _, path := range []string{"/v1/reported-trips", "/v1/reported-trips/7"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if h, _, _ := r.Lookup(method, path); h == nil {
				t.Errorf("%s %s route missing", method, path)
			}
		}
	}
	if _, ok := s.loaders[kindReportedTrip]; !ok {
		t.Error("no loader registered for reported trips")
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
	delete(s.loaders, kindAgency)
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
	delete(s.loaders, kindAgency)
	calls := 0
	rep := map[string]any{"agency_id": agency}
	s.registerLoader(kindAgency, func(_ context.Context, _ *dbgen.Queries, ids []int64) (map[int64]any, error) {
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

func TestGetReportedTrip_ReturnsRepresentation(t *testing.T) {
	pool, agency, id := reportedTripFixture(t)
	router := newReportedTripDetailRouter(t, pool)
	rec := doGet(t, router, reportedTripPath(id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	if rec.Header().Get("Last-Modified") != "" {
		t.Errorf("Last-Modified = %q, want none", rec.Header().Get("Last-Modified"))
	}
	self := reportedTripPath(id)
	want := reportedTrip{
		ReportedTripID: id,
		AgencyID:       agency,
		TripID:         "Trip A",
		Links: links{
			"self":                    self,
			"agency":                  "/v1/agencies/" + strconv.FormatInt(agency, 10),
			"unmatched_trip_episodes": self + "/unmatched-trip-episodes",
		},
	}
	expected, err := encodeJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != string(expected) {
		t.Errorf("body %s, want %s", rec.Body, expected)
	}
}

func TestGetReportedTrip_UnknownIDReturns404(t *testing.T) {
	pool, _, _ := reportedTripFixture(t)
	wantNotFound(t, doGet(t, newReportedTripDetailRouter(t, pool), reportedTripPath(999999), nil))
}

func TestGetReportedTrip_MalformedIDReturns404(t *testing.T) {
	router := newReportedTripDetailRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/reported-trips/"+id, nil))
		})
	}
}

func TestGetReportedTrip_RejectsUnknownParameter(t *testing.T) {
	router := newReportedTripDetailRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/reported-trips/1?foo=1", nil), "foo")
	wantInvalidParam(t, doGet(t, router, "/v1/reported-trips/1?agency_id=1", nil), "agency_id")
	wantInvalidParam(t, doGet(t, router, "/v1/reported-trips/1?include=agency&include=agency", nil), "include")
}

func TestGetReportedTrip_ETag(t *testing.T) {
	pool, _, id := reportedTripFixture(t)
	router := newReportedTripDetailRouter(t, pool)
	path := reportedTripPath(id)

	first := doGet(t, router, path, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	second := doGet(t, router, path, map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetReportedTrip_Head(t *testing.T) {
	pool, _, id := reportedTripFixture(t)
	server := httptest.NewServer(newReportedTripDetailRouter(t, pool))
	defer server.Close()
	url := server.URL + reportedTripPath(id)

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

func TestGetReportedTrip_IncludeCacheControl(t *testing.T) {
	pool, agency, id := reportedTripFixture(t)
	router := newReportedTripDetailRouter(t, pool)

	for _, tc := range []struct {
		query string
		want  string
	}{
		{"", "private, max-age=86400"},
		{"?include=agency", "private, max-age=30"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := doGet(t, router, reportedTripPath(id)+tc.query, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("Cache-Control = %q, want %q", got, tc.want)
			}
			if tc.query != "" {
				var body reportedTripBody
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				agencyPath := "/v1/agencies/" + strconv.FormatInt(agency, 10)
				if _, ok := body.Included[agencyPath]; !ok {
					t.Errorf("included missing %s: %v", agencyPath, body.Included)
				}
			}
		})
	}
}

func TestReportedTripLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	server := dbtest.ID(t, pool, "INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('https://loader.example.com', 'Loader') RETURNING server_id")
	agency := dbtest.ID(t, pool, "INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'L') RETURNING agency_id", server)
	ids := []int64{
		dbtest.ID(t, pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'T1') RETURNING reported_trip_id", agency),
		dbtest.ID(t, pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'T2') RETURNING reported_trip_id", agency),
		dbtest.ID(t, pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'T3') RETURNING reported_trip_id", agency),
	}
	dbtest.ID(t, pool, "INSERT INTO reported_trip (agency_id, oba_trip_id) VALUES ($1, 'not-asked') RETURNING reported_trip_id", agency)

	s, _ := newTestService(t)
	detail := newReportedTripDetailRouter(t, pool)
	db := &countingDB{DBTX: pool}

	reps, err := s.loaders[kindReportedTrip](context.Background(), dbgen.New(db), append(ids, 999999))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(reps) != len(ids) {
		t.Errorf("loader returned %d reported trips, want %d", len(reps), len(ids))
	}

	for _, id := range ids {
		loaded, err := encodeJSON(reps[id])
		if err != nil {
			t.Fatalf("encode loaded %d: %v", id, err)
		}
		rec := doGet(t, detail, reportedTripPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET reported trip %d: status = %d", id, rec.Code)
		}
		if string(loaded) != rec.Body.String() {
			t.Errorf("reported trip %d: loaded\n%s\nGET returned\n%s", id, loaded, rec.Body)
		}
	}
}
