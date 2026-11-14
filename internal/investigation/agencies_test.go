package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newAgenciesRouter returns a router serving listServerAgencies and getAgency,
// and getServer so included servers can be compared with their GET body, on a
// service using
// pool (nil for tests that never reach the database). The service's loaders
// are registered, so include works.
//
// Like newServersRouter, it mounts the handlers without the request pipeline:
// until #197 replaces the authenticate stub, every request through s.get is
// rejected with 401.
func newAgenciesRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return nil }

	router := httprouter.New()
	router.GET("/v1/servers/:server_id/agencies", s.listServerAgencies)
	router.HEAD("/v1/servers/:server_id/agencies", s.listServerAgencies)
	router.GET("/v1/agencies/:agency_id", s.getAgency)
	router.HEAD("/v1/agencies/:agency_id", s.getAgency)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

// insertAgency records an agency and returns its ID. A nil name is stored as
// NULL.
func insertAgency(t *testing.T, pool *pgxpool.Pool, serverID int64, externalID string, name *string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO agency (server_id, external_agency_id, current_name) VALUES ($1, $2, $3) RETURNING agency_id",
		serverID, externalID, name)
}

func agenciesPath(serverID int64) string {
	return "/v1/servers/" + strconv.FormatInt(serverID, 10) + "/agencies"
}

// agencyListBody is the E04 body as a client decodes it.
type agencyListBody struct {
	Items []struct {
		AgencyID         int64             `json:"agency_id"`
		ServerID         int64             `json:"server_id"`
		ExternalAgencyID string            `json:"external_agency_id"`
		Links            map[string]string `json:"links"`
	} `json:"items"`
	Links    map[string]string          `json:"links"`
	Included map[string]json.RawMessage `json:"included"`
}

// decodeAgencies decodes a 200 E04 body.
func decodeAgencies(t *testing.T, rec *httptest.ResponseRecorder) agencyListBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body agencyListBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestListServerAgencies_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/servers/1/agencies"); h == nil {
			t.Errorf("%s /v1/servers/:server_id/agencies is not registered", method)
		}
	}
}

func TestListServerAgencies_RejectsUnknownParameter(t *testing.T) {
	router := newAgenciesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers/1/agencies?foo=1", nil), "foo")
}

func TestListServerAgencies_ExternalIDEmptyOrRepeated(t *testing.T) {
	router := newAgenciesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers/1/agencies?external_agency_id=", nil), "external_agency_id")
	wantInvalidParam(t, doGet(t, router, "/v1/servers/1/agencies?external_agency_id=1&external_agency_id=2", nil), "external_agency_id")
}

func TestListServerAgencies_RejectsBadInclude(t *testing.T) {
	router := newAgenciesRouter(t, nil)
	for _, query := range []string{"include=self", "include=agencies", "include=", "include=server,server", "include=server,"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/servers/1/agencies?"+query, nil), "include")
		})
	}
}

func TestListServerAgencies_MalformedServerIDReturns404(t *testing.T) {
	// No database: a malformed ID must be rejected before any query.
	router := newAgenciesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/servers/"+id+"/agencies", nil))
		})
	}
}

func TestListServerAgencies_UnknownServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertServer(t, pool, "https://example.com", "Example")
	router := newAgenciesRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/servers/999999/agencies", nil))
}

func TestListServerAgencies_KnownServerWithNoAgencies(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	router := newAgenciesRouter(t, pool)

	rec := doGet(t, router, agenciesPath(id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := string(raw["items"]); got != "[]" {
		t.Errorf("items = %s, want []", got)
	}
	if _, ok := raw["included"]; ok {
		t.Error("body has included without an include parameter")
	}
}

func TestListServerAgencies_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO agency (agency_id, server_id, external_agency_id, current_name) OVERRIDING SYSTEM VALUE VALUES (40, 7, '1', 'King County Metro'), (41, 7, '40', NULL)"); err != nil {
		t.Fatalf("insert agencies: %v", err)
	}
	router := newAgenciesRouter(t, pool)

	rec := doGet(t, router, "/v1/servers/7/agencies", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	want := `{"items":[` +
		`{"agency_id":40,"server_id":7,"external_agency_id":"1","current_name":"King County Metro",` +
		`"links":{"self":"/v1/agencies/40","server":"/v1/servers/7"}},` +
		`{"agency_id":41,"server_id":7,"external_agency_id":"40","current_name":null,` +
		`"links":{"self":"/v1/agencies/41","server":"/v1/servers/7"}}],` +
		`"links":{"self":"/v1/servers/7/agencies"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

// TestAgencyItems_NoRows checks an empty list encodes as [], without a
// database.
func TestAgencyItems_NoRows(t *testing.T) {
	items, related := agencyItems([]dbgen.Agency{})
	if data, _ := encodeJSON(items); string(data) != "[]\n" {
		t.Errorf("no rows encode as %s, want []", data)
	}
	if len(related) != 0 {
		t.Errorf("related = %v, want none", related)
	}
}

func TestListServerAgencies_OrderedByID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	// Insert out of ID order, so a missing ORDER BY shows.
	for _, row := range []struct {
		id       int64
		external string
	}{{30, "c"}, {10, "a"}, {20, "b"}} {
		if err := dbtest.Exec(pool,
			"INSERT INTO agency (agency_id, server_id, external_agency_id) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3)",
			row.id, id, row.external); err != nil {
			t.Fatalf("insert agency %d: %v", row.id, err)
		}
	}
	router := newAgenciesRouter(t, pool)

	body := decodeAgencies(t, doGet(t, router, agenciesPath(id), nil))
	var got []int64
	for _, item := range body.Items {
		got = append(got, item.AgencyID)
	}
	if want := []int64{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Errorf("agency_ids = %v, want %v", got, want)
	}
}

func TestListServerAgencies_LookupByExternalID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	this := insertServer(t, pool, "https://this.example.com", "This")
	other := insertServer(t, pool, "https://other.example.com", "Other")
	insertAgency(t, pool, other, "1", nil)
	insertAgency(t, pool, this, "40", nil)
	want := insertAgency(t, pool, this, "1", nil)
	router := newAgenciesRouter(t, pool)

	body := decodeAgencies(t, doGet(t, router, agenciesPath(this)+"?external_agency_id=1", nil))
	if len(body.Items) != 1 || body.Items[0].AgencyID != want || body.Items[0].ServerID != this {
		t.Fatalf("items = %+v, want only agency %d of server %d", body.Items, want, this)
	}
	if got, wantSelf := body.Links["self"], agenciesPath(this)+"?external_agency_id=1"; got != wantSelf {
		t.Errorf("links.self = %q, want %q", got, wantSelf)
	}

	body = decodeAgencies(t, doGet(t, router, agenciesPath(this)+"?external_agency_id=missing", nil))
	if len(body.Items) != 0 {
		t.Errorf("items = %+v, want none", body.Items)
	}
}

// TestListServerAgencies_IncludeServer checks include=server adds the
// server once, exactly as GET /v1/servers/{server_id} returns it.
func TestListServerAgencies_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertAgency(t, pool, id, "1", nil)
	insertAgency(t, pool, id, "40", nil)
	router := newAgenciesRouter(t, pool)

	body := decodeAgencies(t, doGet(t, router, agenciesPath(id)+"?include=server", nil))
	serverURL := "/v1/servers/" + strconv.FormatInt(id, 10)
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(body.Included), body.Included)
	}
	get := doGet(t, router, serverURL, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", serverURL, get.Code)
	}
	if got := string(body.Included[serverURL]) + "\n"; got != get.Body.String() {
		t.Errorf("included server =\n%s\nGET returned\n%s", got, get.Body)
	}

	// With no agencies there is nothing to include, but the key is present.
	empty := insertServer(t, pool, "https://empty.example.com", "Empty")
	rec := doGet(t, router, agenciesPath(empty)+"?include=server", nil)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := string(raw["included"]); got != "{}" {
		t.Errorf("included = %s, want {}", got)
	}
}

func TestListServerAgencies_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertAgency(t, pool, id, "1", nil)
	router := newAgenciesRouter(t, pool)

	first := doGet(t, router, agenciesPath(id), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	second := doGet(t, router, agenciesPath(id), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
}

func TestListServerAgencies_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertAgency(t, pool, id, "1", nil)
	router := newAgenciesRouter(t, pool)
	path := agenciesPath(id)

	get := doGet(t, router, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
}

func agencyPath(agencyID int64) string {
	return "/v1/agencies/" + strconv.FormatInt(agencyID, 10)
}

func TestGetAgency_Registered(t *testing.T) {
	s, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/agencies/1"); h == nil {
			t.Errorf("%s /v1/agencies/:agency_id is not registered", method)
		}
	}
	if _, ok := s.loaders[kindAgency]; !ok {
		t.Error("no loader registered for agencies")
	}
}

func TestGetAgency_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if err := dbtest.Exec(pool,
		"INSERT INTO agency (agency_id, server_id, external_agency_id, current_name) OVERRIDING SYSTEM VALUE VALUES (40, 7, '1', 'King County Metro'), (41, 7, '40', NULL)"); err != nil {
		t.Fatalf("insert agencies: %v", err)
	}
	router := newAgenciesRouter(t, pool)

	for _, tc := range []struct {
		path, want string
	}{
		{"/v1/agencies/40", `{"agency_id":40,"server_id":7,"external_agency_id":"1","current_name":"King County Metro",` +
			`"links":{"self":"/v1/agencies/40","server":"/v1/servers/7"}}` + "\n"},
		{"/v1/agencies/41", `{"agency_id":41,"server_id":7,"external_agency_id":"40","current_name":null,` +
			`"links":{"self":"/v1/agencies/41","server":"/v1/servers/7"}}` + "\n"},
	} {
		rec := doGet(t, router, tc.path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type = %q, want application/json", tc.path, got)
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s: body =\n%s\nwant\n%s", tc.path, got, tc.want)
		}
	}
}

func TestGetAgency_UnknownIDReturns404(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	router := newAgenciesRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/agencies/999999", nil))
}

func TestGetAgency_MalformedIDReturns404(t *testing.T) {
	// No database: a malformed ID must be rejected before any query.
	router := newAgenciesRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/agencies/"+id, nil))
		})
	}
}

func TestGetAgency_RejectsUnknownParameter(t *testing.T) {
	router := newAgenciesRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/agencies/1?foo=1", nil), "foo")
}

func TestGetAgency_RejectsBadInclude(t *testing.T) {
	router := newAgenciesRouter(t, nil)
	for _, query := range []string{"include=self", "include=agency", "include=", "include=server,server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/agencies/1?"+query, nil), "include")
		})
	}
}

// TestGetAgency_IncludeServer checks include=server adds the agency's server,
// exactly as GET /v1/servers/{server_id} returns it.
func TestGetAgency_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	serverID := insertServer(t, pool, "https://example.com", "Example")
	id := insertAgency(t, pool, serverID, "1", nil)
	router := newAgenciesRouter(t, pool)

	rec := doGet(t, router, agencyPath(id)+"?include=server", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body struct {
		AgencyID int64                      `json:"agency_id"`
		Included map[string]json.RawMessage `json:"included"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.AgencyID != id {
		t.Errorf("agency_id = %d, want %d", body.AgencyID, id)
	}
	serverURL := "/v1/servers/" + strconv.FormatInt(serverID, 10)
	if len(body.Included) != 1 {
		t.Fatalf("included has %d entries, want 1: %v", len(body.Included), body.Included)
	}
	get := doGet(t, router, serverURL, nil)
	if got := string(body.Included[serverURL]) + "\n"; got != get.Body.String() {
		t.Errorf("included server =\n%s\nGET returned\n%s", got, get.Body)
	}
}

func TestGetAgency_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	router := newAgenciesRouter(t, pool)

	first := doGet(t, router, agencyPath(id), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", first.Code, first.Body)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag header")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
	second := doGet(t, router, agencyPath(id), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body)
	}
}

func TestGetAgency_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertAgency(t, pool, insertServer(t, pool, "https://example.com", "Example"), "1", nil)
	router := newAgenciesRouter(t, pool)
	path := agencyPath(id)

	get := doGet(t, router, path, nil)
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))

	if head.Code != get.Code {
		t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
	}
	if !reflect.DeepEqual(head.Header(), get.Header()) {
		t.Errorf("HEAD headers = %v, GET headers = %v", head.Header(), get.Header())
	}
	if get.Body.Len() == 0 {
		t.Error("GET body is empty")
	}
}

func TestAgencyLoader(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	a := insertServer(t, pool, "https://a.example.com", "A")
	b := insertServer(t, pool, "https://b.example.com", "B")
	name := "Metro"
	ids := []int64{
		insertAgency(t, pool, a, "1", &name),
		insertAgency(t, pool, a, "40", nil),
		insertAgency(t, pool, b, "1", nil),
	}
	insertAgency(t, pool, b, "not-asked", nil)
	router := newAgenciesRouter(t, pool)

	s, _ := newTestService(t)
	db := &countingDB{DBTX: pool}
	got, err := s.loaders[kindAgency](context.Background(), dbgen.New(db), ids)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if db.queries != 1 {
		t.Errorf("loader ran %d queries, want 1", db.queries)
	}
	if len(got) != len(ids) {
		t.Errorf("loader returned %d agencies, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		data, err := encodeJSON(got[id])
		if err != nil {
			t.Fatalf("encode agency %d: %v", id, err)
		}
		rec := doGet(t, router, agencyPath(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET agency %d: status = %d", id, rec.Code)
		}
		if string(data) != rec.Body.String() {
			t.Errorf("agency %d: loaded\n%s\nGET returned\n%s", id, data, rec.Body)
		}
	}
}
