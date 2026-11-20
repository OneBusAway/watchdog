package investigation

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbtest"
)

const clustersPath = "/v1/unmatched-stop-clusters"

// newClustersRouter mounts E38 at episodesNow.
func newClustersRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	s, _ := newTestService(t)
	s.Pool = pool
	s.now = func() time.Time { return episodesNow }
	router := httprouter.New()
	router.GET(clustersPath, s.listUnmatchedStopClusters)
	router.HEAD(clustersPath, s.listUnmatchedStopClusters)
	return router
}

// seedScheme inserts a cluster scheme at level 13, created at created.
func seedScheme(t *testing.T, pool *pgxpool.Pool, version string, created time.Time) {
	t.Helper()
	if err := dbtest.Exec(pool,
		`INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description, created_at)
		 VALUES ($1, 13, 'root_station', 'Test scheme', $2)`, version, created); err != nil {
		t.Fatalf("insert scheme: %v", err)
	}
}

// seedCell inserts a level-13 cell of scheme version.
func seedCell(t *testing.T, pool *pgxpool.Pool, version, cellID string) {
	t.Helper()
	if err := dbtest.Exec(pool,
		`INSERT INTO s2_cell (scheme_version, cell_id, cell_level, center_latitude, center_longitude)
		 VALUES ($1, $2, 13, 47.6097, -122.3331)`, version, cellID); err != nil {
		t.Fatalf("insert cell: %v", err)
	}
}

// linkVersion links episodeID to a static version published at published, with one
// stop metadata row, and returns that row's ID.
func linkVersion(t *testing.T, s stop, episodeID int64, published time.Time) int64 {
	t.Helper()
	versionID := dbtest.ID(t, s.pool,
		"INSERT INTO gtfs_static_version (server_id, published_at) VALUES ($1, $2) RETURNING gtfs_static_version_id",
		s.serverID, published)
	metadataID := dbtest.ID(t, s.pool,
		`INSERT INTO stop_metadata_version (gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found)
		 VALUES ($1, $2, $3, true) RETURNING stop_metadata_version_id`,
		versionID, s.agencyID, s.reportedStopID)
	if err := dbtest.Exec(s.pool,
		`INSERT INTO unmatched_stop_episode_metadata (unmatched_stop_episode_id, stop_metadata_version_id, reported_stop_id, agency_id)
		 VALUES ($1, $2, $3, $4)`,
		episodeID, metadataID, s.reportedStopID, s.agencyID); err != nil {
		t.Fatalf("link version: %v", err)
	}
	return metadataID
}

// assign puts metadataID in cell of scheme, under station (nil for no station).
func assign(t *testing.T, pool *pgxpool.Pool, metadataID int64, scheme, cell string, station *string) {
	t.Helper()
	var name *string
	if station != nil {
		n := "Station " + *station
		name = &n
	}
	if err := dbtest.Exec(pool,
		`INSERT INTO stop_cluster_assignment (stop_metadata_version_id, scheme_version, cell_id, root_station_id, root_station_name)
		 VALUES ($1, $2, $3, $4, $5)`, metadataID, scheme, cell, station, name); err != nil {
		t.Fatalf("assign: %v", err)
	}
}

func strPtr(v string) *string { return &v }

// clusterItems decodes the items of an E38 response.
func clusterItems(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items = %v, want an array", body["items"])
	}
	items := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		items = append(items, it.(map[string]any))
	}
	return items
}

// groupCounts lists each item's cell_id, root_station_id and open_episode_count, in order.
func groupCounts(t *testing.T, items []map[string]any) [][3]any {
	t.Helper()
	out := make([][3]any, 0, len(items))
	for _, it := range items {
		out = append(out, [3]any{it["cell_id"], it["root_station_id"], it["open_episode_count"]})
	}
	return out
}

// clustersAt requests E38 for serverID at at, with extra query text appended.
func clustersAt(serverID int64, at time.Time, extra string) string {
	q := fmt.Sprintf("server_id=%d&at=%s", serverID, url.QueryEscape(at.Format(time.RFC3339Nano)))
	if extra != "" {
		q += "&" + extra
	}
	return clustersPath + "?" + q
}

func TestListUnmatchedStopClusters_Groups(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	seedCell(t, pool, "s", "c2")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opened := at.Add(-time.Hour)

	// Four stops on one server: three episodes in c1 under station 1_A, one in c2 with no station.
	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	for i := range 4 {
		st := addStop(t, pool, serverID, agencyID, fmt.Sprintf("1_%d", i))
		id := st.episode(t, opened, time.Time{})
		if i < 3 {
			assign(t, pool, linkVersion(t, st, id, opened.Add(-time.Hour)), "s", "c1", strPtr("1_A"))
		} else {
			assign(t, pool, linkVersion(t, st, id, opened.Add(-time.Hour)), "s", "c2", nil)
		}
	}

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(serverID, at, "scheme_version=s"), nil))
	items := clusterItems(t, body)
	want := [][3]any{{"c1", "1_A", float64(3)}, {"c2", nil, float64(1)}}
	if got := groupCounts(t, items); !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestListUnmatchedStopClusters_OpenAtTime(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	// One stop per episode: a stop may have only one open episode.
	// Open at at: included.
	openStop := addStop(t, pool, serverID, agencyID, "1_open")
	openNow := openStop.episode(t, at.Add(-time.Hour), time.Time{})
	assign(t, pool, linkVersion(t, openStop, openNow, at.Add(-2*time.Hour)), "s", "c1", nil)
	// Closed before at: excluded.
	closedStop := addStop(t, pool, serverID, agencyID, "1_closed")
	closedBefore := closedStop.episode(t, at.Add(-3*time.Hour), at.Add(-time.Minute))
	assign(t, pool, linkVersion(t, closedStop, closedBefore, at.Add(-2*time.Hour)), "s", "c1", nil)
	// Opened after at: excluded.
	laterStop := addStop(t, pool, serverID, agencyID, "1_later")
	openedAfter := laterStop.episode(t, at.Add(time.Hour), time.Time{})
	assign(t, pool, linkVersion(t, laterStop, openedAfter, at.Add(-2*time.Hour)), "s", "c1", nil)

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(serverID, at, "scheme_version=s"), nil))
	want := [][3]any{{"c1", nil, float64(1)}}
	if got := groupCounts(t, clusterItems(t, body)); !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestListUnmatchedStopClusters_ContextAtTime(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	seedCell(t, pool, "s", "c2")
	opened := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	v1Published := opened.Add(-time.Hour)
	v2Published := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	// The stop moved from c1 to c2 when its static version changed at v2Published.
	st := newStop(t, pool)
	id := st.episode(t, opened, time.Time{})
	assign(t, pool, linkVersion(t, st, id, v1Published), "s", "c1", nil)
	assign(t, pool, linkVersion(t, st, id, v2Published), "s", "c2", nil)

	router := newClustersRouter(t, pool)
	before := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)

	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, before, "scheme_version=s"), nil))
	if got, want := groupCounts(t, clusterItems(t, body)), [][3]any{{"c1", nil, float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("at %v groups = %v, want %v", before, got, want)
	}
	body = decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, after, "scheme_version=s"), nil))
	if got, want := groupCounts(t, clusterItems(t, body)), [][3]any{{"c2", nil, float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("at %v groups = %v, want %v", after, got, want)
	}
}

func TestListUnmatchedStopClusters_Unassigned(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opened := at.Add(-time.Hour)

	serverID := insertEpisodeServer(t, pool, "https://example.com")
	agencyID := insertEpisodeAgency(t, pool, serverID, "1")
	assignedStop := addStop(t, pool, serverID, agencyID, "1_assigned")
	assigned := assignedStop.episode(t, opened, time.Time{})
	assign(t, pool, linkVersion(t, assignedStop, assigned, opened.Add(-time.Hour)), "s", "c1", nil)
	// No static context at all: unassigned.
	addStop(t, pool, serverID, agencyID, "1_unlinked").episode(t, opened, time.Time{})

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(serverID, at, "scheme_version=s"), nil))
	items := clusterItems(t, body)
	if len(items) != 2 {
		t.Fatalf("items = %v, want 2 groups", items)
	}
	last := items[1]
	if last["cell_id"] != nil || last["root_station_id"] != nil || last["cell_level"] != nil ||
		last["center_latitude"] != nil || last["center_longitude"] != nil || last["root_station_name"] != nil {
		t.Errorf("unassigned group has non-null cell or station fields: %v", last)
	}
	if last["open_episode_count"] != float64(1) {
		t.Errorf("unassigned count = %v, want 1", last["open_episode_count"])
	}
	if links, ok := last["links"].(map[string]any); !ok || len(links) != 0 {
		t.Errorf("unassigned links = %v, want none", last["links"])
	}
}

func TestListUnmatchedStopClusters_NoSchemeIsAllUnassigned(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newStop(t, pool)
	st.episode(t, at.Add(-time.Hour), time.Time{})

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, at, ""), nil))
	if body["scheme_version"] != nil {
		t.Errorf("scheme_version = %v, want null", body["scheme_version"])
	}
	if got, want := groupCounts(t, clusterItems(t, body)), [][3]any{{nil, nil, float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestListUnmatchedStopClusters_DefaultSchemeIsNewest(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "older", episodesNow.Add(-2*time.Hour))
	seedScheme(t, pool, "newer", episodesNow.Add(-time.Hour))
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newStop(t, pool)
	st.episode(t, at.Add(-time.Hour), time.Time{})

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, at, ""), nil))
	if body["scheme_version"] != "newer" {
		t.Errorf("scheme_version = %v, want newer", body["scheme_version"])
	}
}

func TestListUnmatchedStopClusters_EpisodesLink(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newStop(t, pool)
	id := st.episode(t, at.Add(-time.Hour), time.Time{})
	assign(t, pool, linkVersion(t, st, id, at.Add(-2*time.Hour)), "s", "c1", strPtr("1_A"))

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, at, "scheme_version=s"), nil))
	item := clusterItems(t, body)[0]
	links := item["links"].(map[string]any)
	want := fmt.Sprintf("/v1/unmatched-stop-episodes?server_id=%d&open_at=2026-10-01T12:00:00Z&scheme_version=s&cell_id=c1&root_station_id=1_A", st.serverID)
	if links["episodes"] != want {
		t.Errorf("episodes link = %v, want %v", links["episodes"], want)
	}
}

func TestListUnmatchedStopClusters_RequiresParams(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClustersRouter(t, pool)
	at := episodesNow.Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, clustersPath+"?at="+at, nil), "server_id")
	wantInvalidParam(t, doGet(t, router, clustersPath+"?server_id=1", nil), "at")
	future := episodesNow.Add(time.Hour).Format(time.RFC3339)
	wantInvalidParam(t, doGet(t, router, clustersPath+"?server_id=1&at="+future, nil), "at")
}

func TestListUnmatchedStopClusters_UnknownScheme(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClustersRouter(t, pool)
	at := url.QueryEscape(episodesNow.Format(time.RFC3339))
	wantInvalidParam(t, doGet(t, router, clustersPath+"?server_id=1&at="+at+"&scheme_version=nope", nil), "scheme_version")
}

func TestListUnmatchedStopClusters_RejectsUnknownParameter(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClustersRouter(t, pool)
	at := url.QueryEscape(episodesNow.Format(time.RFC3339))
	wantInvalidParam(t, doGet(t, router, clustersPath+"?server_id=1&at="+at+"&include=server", nil), "include")
}

func TestListUnmatchedStopClusters_Head(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClustersRouter(t, pool)
	at := url.QueryEscape(episodesNow.Format(time.RFC3339))
	path := clustersPath + "?server_id=1&at=" + at
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
}

func TestListUnmatchedStopClusters_CachesShort(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	router := newClustersRouter(t, pool)
	at := url.QueryEscape(episodesNow.Format(time.RFC3339))
	rec := doGet(t, router, clustersPath+"?server_id=1&at="+at, nil)
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want private, max-age=30", got)
	}
}

func TestListUnmatchedStopClusters_Representation(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	seedScheme(t, pool, "s", episodesNow.Add(-time.Hour))
	seedCell(t, pool, "s", "c1")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := newStop(t, pool)
	id := st.episode(t, at.Add(-time.Hour), time.Time{})
	assign(t, pool, linkVersion(t, st, id, at.Add(-2*time.Hour)), "s", "c1", strPtr("1_A"))

	router := newClustersRouter(t, pool)
	body := decodeEpisodeBody(t, doGet(t, router, clustersAt(st.serverID, at, "scheme_version=s"), nil))

	// The top level has exactly the spec's fields.
	wantTop := []string{"agency_id", "at", "items", "links", "scheme_version", "server_id"}
	if got := sortedKeys(body); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("top-level keys = %v, want %v", got, wantTop)
	}
	item := clusterItems(t, body)[0]
	wantItem := []string{"cell_id", "cell_level", "center_latitude", "center_longitude", "links",
		"open_episode_count", "root_station_id", "root_station_name"}
	if got := sortedKeys(item); !reflect.DeepEqual(got, wantItem) {
		t.Errorf("item keys = %v, want %v", got, wantItem)
	}
	if item["cell_level"] != float64(13) || item["root_station_name"] != "Station 1_A" {
		t.Errorf("item = %v, want cell_level 13 and root_station_name Station 1_A", item)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
