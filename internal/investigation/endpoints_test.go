package investigation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// newEndpointsRouter returns a router serving
// listServerVehiclePositionEndpoints, and getServer so included servers can
// be compared with their GET body, on a service using pool (nil for tests
// that never reach the database) and servers as its current configuration.
//
// Like newServersRouter, it mounts the handlers without the request pipeline:
// until #197 replaces the authenticate stub, every request through s.get is
// rejected with 401.
func newEndpointsRouter(t *testing.T, pool *pgxpool.Pool, servers ...models.ObaServer) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.Servers = func() []models.ObaServer { return servers }

	router := httprouter.New()
	router.GET("/v1/servers/:server_id/vehicle-position-endpoints", s.listServerVehiclePositionEndpoints)
	router.HEAD("/v1/servers/:server_id/vehicle-position-endpoints", s.listServerVehiclePositionEndpoints)
	router.GET("/v1/servers/:server_id", s.getServer)
	return router
}

// insertEndpoint records a vehicle-position URL for a server and returns its
// ID.
func insertEndpoint(t *testing.T, pool *pgxpool.Pool, serverID int64, normalizedURL string) int64 {
	t.Helper()
	return dbtest.ID(t, pool,
		"INSERT INTO vehicle_position_endpoint (server_id, normalized_vehicle_position_url) VALUES ($1, $2) RETURNING endpoint_id",
		serverID, normalizedURL)
}

func endpointsPath(serverID int64) string {
	return "/v1/servers/" + strconv.FormatInt(serverID, 10) + "/vehicle-position-endpoints"
}

// configuredServer is a configuration entry for baseURL fetching the given
// vehicle-position URLs.
func configuredServer(baseURL string, vehiclePositionURLs ...string) models.ObaServer {
	feeds := make([]models.GtfsRTFeed, 0, len(vehiclePositionURLs))
	for _, u := range vehiclePositionURLs {
		feeds = append(feeds, models.GtfsRTFeed{VehiclePositionURL: u})
	}
	return models.ObaServer{ObaBaseURL: baseURL, GtfsRTFeeds: feeds}
}

// endpointListBody is the E06 body as a client decodes it.
type endpointListBody struct {
	Items []struct {
		EndpointID int64             `json:"endpoint_id"`
		Configured bool              `json:"configured"`
		Links      map[string]string `json:"links"`
	} `json:"items"`
	Links    map[string]string          `json:"links"`
	Included map[string]json.RawMessage `json:"included"`
}

// decodeEndpoints decodes a 200 E06 body.
func decodeEndpoints(t *testing.T, rec *httptest.ResponseRecorder) endpointListBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var body endpointListBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestListServerVehiclePositionEndpoints_Registered(t *testing.T) {
	_, router := newTestService(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if h, _, _ := router.Lookup(method, "/v1/servers/1/vehicle-position-endpoints"); h == nil {
			t.Errorf("%s /v1/servers/:server_id/vehicle-position-endpoints is not registered", method)
		}
	}
}

func TestListServerVehiclePositionEndpoints_RejectsUnknownParameter(t *testing.T) {
	router := newEndpointsRouter(t, nil)
	wantInvalidParam(t, doGet(t, router, "/v1/servers/1/vehicle-position-endpoints?foo=1", nil), "foo")
}

func TestListServerVehiclePositionEndpoints_RejectsBadInclude(t *testing.T) {
	router := newEndpointsRouter(t, nil)
	for _, query := range []string{"include=self", "include=vehicle_anomaly_episodes", "include=", "include=server,server"} {
		t.Run(query, func(t *testing.T) {
			wantInvalidParam(t, doGet(t, router, "/v1/servers/1/vehicle-position-endpoints?"+query, nil), "include")
		})
	}
}

func TestListServerVehiclePositionEndpoints_MalformedServerIDReturns404(t *testing.T) {
	// No database: a malformed ID must be rejected before any query.
	router := newEndpointsRouter(t, nil)
	for _, id := range []string{"abc", "0", "-1", "07"} {
		t.Run(id, func(t *testing.T) {
			wantNotFound(t, doGet(t, router, "/v1/servers/"+id+"/vehicle-position-endpoints", nil))
		})
	}
}

func TestListServerVehiclePositionEndpoints_UnknownServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	insertServer(t, pool, "https://example.com", "Example")
	router := newEndpointsRouter(t, pool)
	wantNotFound(t, doGet(t, router, "/v1/servers/999999/vehicle-position-endpoints", nil))
}

func TestListServerVehiclePositionEndpoints_KnownServerWithNoEndpoints(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	router := newEndpointsRouter(t, pool)

	rec := doGet(t, router, endpointsPath(id), nil)
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

func TestListServerVehiclePositionEndpoints_ReturnsRepresentation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (server_id, normalized_base_url, display_name) OVERRIDING SYSTEM VALUE VALUES (7, 'https://example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	created := time.Date(2026, 10, 24, 14, 5, 30, 123456000, time.UTC)
	if err := dbtest.Exec(pool,
		"INSERT INTO vehicle_position_endpoint (endpoint_id, server_id, normalized_vehicle_position_url, created_at) OVERRIDING SYSTEM VALUE VALUES (50, 7, 'https://feeds.example.com/vp', $1)",
		created); err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	router := newEndpointsRouter(t, pool,
		configuredServer("https://example.com", "https://feeds.example.com/vp?key=secret"))

	rec := doGet(t, router, "/v1/servers/7/vehicle-position-endpoints", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	want := `{"items":[{"endpoint_id":50,"server_id":7,` +
		`"normalized_vehicle_position_url":"https://feeds.example.com/vp",` +
		`"created_at":"2026-10-24T14:05:30.123456Z","configured":true,"links":{` +
		`"self":"/v1/vehicle-position-endpoints/50",` +
		`"server":"/v1/servers/7",` +
		`"vehicle_anomaly_episodes":"/v1/vehicle-anomaly-episodes?endpoint_id=50"}}],` +
		`"links":{"self":"/v1/servers/7/vehicle-position-endpoints"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestListServerVehiclePositionEndpoints_OrderedByID(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	// Insert out of ID order, so a missing ORDER BY shows.
	for _, row := range []struct {
		id  int64
		url string
	}{{30, "https://c.example.com"}, {10, "https://a.example.com"}, {20, "https://b.example.com"}} {
		if err := dbtest.Exec(pool,
			"INSERT INTO vehicle_position_endpoint (endpoint_id, server_id, normalized_vehicle_position_url) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3)",
			row.id, id, row.url); err != nil {
			t.Fatalf("insert endpoint %d: %v", row.id, err)
		}
	}
	router := newEndpointsRouter(t, pool)

	body := decodeEndpoints(t, doGet(t, router, endpointsPath(id), nil))
	var got []int64
	for _, item := range body.Items {
		got = append(got, item.EndpointID)
	}
	if want := []int64{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Errorf("endpoint_ids = %v, want %v", got, want)
	}
}

func TestListServerVehiclePositionEndpoints_Configured(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	other := insertServer(t, pool, "https://other.example.com", "Other")
	current := insertEndpoint(t, pool, id, "https://feeds.example.com/new")
	old := insertEndpoint(t, pool, id, "https://feeds.example.com/old")
	// A second agency entry of the same server fetches this one.
	secondAgency := insertEndpoint(t, pool, id, "https://feeds.example.com/agency-b")
	// Another server is configured with this URL, but not this server.
	elsewhere := insertEndpoint(t, pool, id, "https://feeds.example.com/shared")
	insertEndpoint(t, pool, other, "https://feeds.example.com/shared")

	// The configuration holds the URLs unnormalized, as config.json would.
	router := newEndpointsRouter(t, pool,
		configuredServer("https://example.com?key=secret", "https://feeds.example.com/new?key=secret"),
		configuredServer("https://example.com", "https://feeds.example.com/agency-b"),
		configuredServer("https://other.example.com", "https://feeds.example.com/shared"),
	)

	body := decodeEndpoints(t, doGet(t, router, endpointsPath(id), nil))
	got := map[int64]bool{}
	for _, item := range body.Items {
		got[item.EndpointID] = item.Configured
	}
	want := map[int64]bool{current: true, old: false, secondAgency: true, elsewhere: false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("configured = %v, want %v", got, want)
	}
}

// TestListServerVehiclePositionEndpoints_IncludeServer checks include=server
// adds the server once, exactly as GET /v1/servers/{server_id} returns it.
func TestListServerVehiclePositionEndpoints_IncludeServer(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertEndpoint(t, pool, id, "https://feeds.example.com/a")
	insertEndpoint(t, pool, id, "https://feeds.example.com/b")
	router := newEndpointsRouter(t, pool)

	body := decodeEndpoints(t, doGet(t, router, endpointsPath(id)+"?include=server", nil))
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
}

func TestListServerVehiclePositionEndpoints_ETag(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertEndpoint(t, pool, id, "https://feeds.example.com/vp")
	router := newEndpointsRouter(t, pool)

	first := doGet(t, router, endpointsPath(id), nil)
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
	second := doGet(t, router, endpointsPath(id), map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
}

func TestListServerVehiclePositionEndpoints_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	id := insertServer(t, pool, "https://example.com", "Example")
	insertEndpoint(t, pool, id, "https://feeds.example.com/vp")
	router := newEndpointsRouter(t, pool)
	path := endpointsPath(id)

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

// TestConfiguredVehiclePositionURLs checks the configured set without a
// database: only the given server's entries count, URLs are normalized, and
// a feed with no vehicle-position URL adds nothing.
func TestConfiguredVehiclePositionURLs(t *testing.T) {
	servers := []models.ObaServer{
		configuredServer("https://example.com/api?key=x", "https://feeds.example.com/a?key=y", ""),
		configuredServer("https://example.com/api", "https://feeds.example.com/b"),
		configuredServer("https://other.example.com", "https://feeds.example.com/c"),
	}
	got := configuredVehiclePositionURLs(servers, "https://example.com/api")
	want := map[string]bool{"https://feeds.example.com/a": true, "https://feeds.example.com/b": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("configured = %v, want %v", got, want)
	}
}
