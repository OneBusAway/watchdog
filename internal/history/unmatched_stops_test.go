package history

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// stopServer is a server-scoped entry: it records every agency it is given.
var stopServer = models.ObaServer{
	ServerName: "Stop Transit",
	ObaBaseURL: "https://api.stop-transit.example",
}

var stopT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func stopAgency(agencyID string) models.ObaServer {
	agency := stopServer
	agency.AgencyID = agencyID
	agency.AgencyName = "Agency " + agencyID
	return agency
}

func stopAgencies(agencyIDs ...string) []models.ObaServer {
	agencies := make([]models.ObaServer, len(agencyIDs))
	for i, id := range agencyIDs {
		agencies[i] = stopAgency(id)
	}
	return agencies
}

// stopsResponse builds a response whose agencyIDs are complete and whose
// unmatched stop lists and counts are unmatched.
func stopsResponse(complete []string, unmatched map[string][]string) *metrics.OBAMetrics {
	response := &metrics.OBAMetrics{CurrentTime: stopT0.UnixMilli(), Version: 2}
	response.Data.Entry.AgencyIDs = complete
	response.Data.Entry.StopIDsUnmatched = unmatched
	counts := make(map[string]int, len(unmatched))
	for agencyID, ids := range unmatched {
		counts[agencyID] = len(ids)
	}
	response.Data.Entry.StopIDsUnmatchedCount = counts
	return response
}

// queryCounter records the SQL of every statement run on a pool, including
// BEGIN and COMMIT.
type queryCounter struct {
	mu   sync.Mutex
	sqls []string
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = append(c.sqls, data.SQL)
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = nil
}

func (c *queryCounter) count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, sql := range c.sqls {
		if strings.Contains(sql, substr) {
			n++
		}
	}
	return n
}

func newCountedHistory(t *testing.T) (*HistoryService, *pgxpool.Pool, *queryCounter) {
	t.Helper()
	counter := &queryCounter{}
	pool := newMigratedPoolWithTracer(t, counter)
	counter.reset()
	return NewHistoryService(pool, discardLogger(), "test-build", nil), pool, counter
}

func memoryEpisode(t *testing.T, h *HistoryService, agencyID, stopID string) *openStopEpisode {
	t.Helper()
	entry := h.stops.forServer(utils.SanitizeServerURL(stopServer.ObaBaseURL), stopServer)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.open[unmatchedStopKey{agencyID: agencyID, stopID: stopID}]
}

type storedEpisode struct {
	stopID         string
	lastReportedAt time.Time
	closeReason    *string
	openedAt       time.Time
	closedAt       *time.Time
}

func storedEpisodes(t *testing.T, pool *pgxpool.Pool) []storedEpisode {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT s.oba_stop_id, e.last_reported_at, e.close_reason, opening.observed_at, closing.observed_at
		FROM unmatched_stop_episode e
		JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id
		JOIN onebusaway_metrics_response_agency oa ON oa.response_agency_id = e.first_response_agency_id
		JOIN onebusaway_metrics_response opening ON opening.response_id = oa.response_id
		LEFT JOIN onebusaway_metrics_response_agency ca ON ca.response_agency_id = e.closing_response_agency_id
		LEFT JOIN onebusaway_metrics_response closing ON closing.response_id = ca.response_id
		ORDER BY e.unmatched_stop_episode_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var episodes []storedEpisode
	for rows.Next() {
		var e storedEpisode
		if err := rows.Scan(&e.stopID, &e.lastReportedAt, &e.closeReason, &e.openedAt, &e.closedAt); err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return episodes
}

func onlyEpisode(t *testing.T, pool *pgxpool.Pool) storedEpisode {
	t.Helper()
	episodes := storedEpisodes(t, pool)
	if len(episodes) != 1 {
		t.Fatalf("got %d episodes, want 1: %+v", len(episodes), episodes)
	}
	return episodes[0]
}

// withStaticVersion records a static version for stopServer, makes it current
// and publishes stops as agency "a"'s snapshot.
func withStaticVersion(t *testing.T, h *HistoryService, pool *pgxpool.Pool, store *gtfs.StaticStore, publishedAt time.Time, stops ...remoteGtfs.Stop) int64 {
	t.Helper()
	var versionID int64
	inTx(t, pool, func(q *dbgen.Queries) error {
		serverID, err := ensureServer(ctx, q, stopServer, nil)
		if err != nil {
			return err
		}
		versionID, err = q.InsertGTFSStaticVersion(ctx, dbgen.InsertGTFSStaticVersionParams{ServerID: serverID, PublishedAt: publishedAt})
		return err
	})
	store.Set(models.ServerKey(stopServer.ObaBaseURL, "a"), &models.StaticData{Stops: stops})
	h.SetStaticStore(store)
	snapshot, _ := store.Get(models.ServerKey(stopServer.ObaBaseURL, "a"))
	h.static.set(stopServer.ServerKey(), publishedAt, staticVersion{
		id:        versionID,
		snapshots: map[string]*models.StaticData{models.ServerKey(stopServer.ObaBaseURL, "a"): snapshot},
	}, nil)
	return versionID
}

func floatPtr(v float64) *float64 { return &v }

func testStop(id, name string, parent *remoteGtfs.Stop) remoteGtfs.Stop {
	return remoteGtfs.Stop{Id: id, Name: name, Type: 0, Latitude: floatPtr(47.6), Longitude: floatPtr(-122.3), Parent: parent}
}

func TestPlanStopEpisodes(t *testing.T) {
	open := map[unmatchedStopKey]*openStopEpisode{
		{agencyID: "a", stopID: "1"}: {},
		{agencyID: "a", stopID: "2"}: {},
		{agencyID: "b", stopID: "9"}: {},
	}
	response := stopsResponse([]string{"a"}, map[string][]string{"a": {"1", "3", "3", ""}, "b": {"8"}})

	plan := planStopEpisodes(open, stopAgencies("a", "b", "a"), response)

	if len(plan.agencies) != 2 {
		t.Errorf("agencies = %d, want 2 without duplicates", len(plan.agencies))
	}
	if !plan.complete["a"] || plan.complete["b"] {
		t.Errorf("complete = %v, want only a", plan.complete)
	}
	if want := []unmatchedStopKey{{"a", "3"}}; !equalKeys(plan.opens, want) {
		t.Errorf("opens = %v, want %v", plan.opens, want)
	}
	if want := []unmatchedStopKey{{"a", "2"}}; !equalKeys(plan.closes, want) {
		t.Errorf("closes = %v, want %v", plan.closes, want)
	}
	if want := []unmatchedStopKey{{"a", "1"}}; !equalKeys(plan.still, want) {
		t.Errorf("still = %v, want %v", plan.still, want)
	}
}

func equalKeys(a, b []unmatchedStopKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRecordOBAMetrics_NilPoolNoop(t *testing.T) {
	h := NewHistoryService(nil, discardLogger(), "test-build", nil)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)
	h.Flush(ctx)
	h.FlushServer(ctx, stopServer.ObaBaseURL)
}

func TestRecordOBAMetrics_Opens(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	withStaticVersion(t, h, pool, gtfs.NewStaticStore(), stopT0.Add(-time.Hour), testStop("1", "Main St", nil))

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)

	episode := onlyEpisode(t, pool)
	if episode.stopID != "1" || !episode.openedAt.Equal(stopT0) || !episode.lastReportedAt.Equal(stopT0) || episode.closeReason != nil {
		t.Errorf("episode = %+v, want stop 1 open at %v", episode, stopT0)
	}
	var complete bool
	var unmatchedStops *int32
	var upstream *time.Time
	var apiVersion *int32
	if err := pool.QueryRow(ctx, `
		SELECT ra.response_complete, ra.unmatched_stop_count, r.upstream_current_time, r.api_version
		FROM onebusaway_metrics_response_agency ra
		JOIN onebusaway_metrics_response r ON r.response_id = ra.response_id`).Scan(&complete, &unmatchedStops, &upstream, &apiVersion); err != nil {
		t.Fatal(err)
	}
	if !complete || unmatchedStops == nil || *unmatchedStops != 1 {
		t.Errorf("response agency: complete=%v unmatched_stop_count=%v, want true and 1", complete, unmatchedStops)
	}
	if upstream == nil || !upstream.Equal(stopT0) || apiVersion == nil || *apiVersion != 2 {
		t.Errorf("response: upstream_current_time=%v api_version=%v", upstream, apiVersion)
	}
	var found bool
	var name *string
	if err := pool.QueryRow(ctx, `
		SELECT v.static_stop_found, v.stop_name
		FROM unmatched_stop_episode_metadata m
		JOIN stop_metadata_version v ON v.stop_metadata_version_id = m.stop_metadata_version_id`).Scan(&found, &name); err != nil {
		t.Fatal(err)
	}
	if !found || name == nil || *name != "Main St" {
		t.Errorf("metadata: found=%v name=%v, want true and Main St", found, name)
	}
}

func TestRecordOBAMetrics_StillReported(t *testing.T) {
	h, pool, counter := newCountedHistory(t)
	response := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, stopT0)
	counter.reset()

	later := stopT0.Add(30 * time.Second)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, later)

	if n := counter.count(""); n != 0 {
		t.Fatalf("still-reported poll ran %d statements, want 0", n)
	}
	if got := memoryEpisode(t, h, "a", "1").lastReportedAt; !got.Equal(later) {
		t.Errorf("last reported in memory = %v, want %v", got, later)
	}
	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(stopT0) {
		t.Errorf("stored last_reported_at = %v, want %v until the save is due", got, stopT0)
	}
}

func TestRecordOBAMetrics_QuietPollNoDatabase(t *testing.T) {
	h, _, counter := newCountedHistory(t)
	quiet := stopsResponse([]string{"a"}, map[string][]string{})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), quiet, stopT0)
	counter.reset()

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), quiet, stopT0.Add(30*time.Second))

	if n := counter.count(""); n != 0 {
		t.Fatalf("quiet poll ran %d statements, want 0", n)
	}
}

func TestRecordOBAMetrics_LoadsOpenEpisodesOnce(t *testing.T) {
	h, pool, counter := newCountedHistory(t)
	response := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	for i := 0; i < 3; i++ {
		h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, stopT0.Add(time.Duration(i)*30*time.Second))
	}
	if n := counter.count("ListOpenUnmatchedStopEpisodesForServer"); n != 1 {
		t.Fatalf("loaded open episodes %d times, want 1", n)
	}

	// After a restart the open episode is loaded, not opened again.
	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	restarted.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, stopT0.Add(2*time.Minute))
	if n := count(t, pool, "unmatched_stop_episode"); n != 1 {
		t.Fatalf("got %d episodes after restart, want 1", n)
	}
	if memoryEpisode(t, restarted, "a", "1") == nil {
		t.Fatal("restarted service did not load the open episode")
	}
}

func TestRecordOBAMetrics_FailedWriteRecordedNextPoll(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	quiet := stopsResponse([]string{"a"}, map[string][]string{})
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	h.RecordOBAMetrics(ctx, stopServer, agencies, quiet, stopT0)
	h.RecordOBAMetrics(canceled, stopServer, agencies, reported, stopT0.Add(1*time.Minute))
	if n := count(t, pool, "unmatched_stop_episode"); n != 0 {
		t.Fatalf("failed write left %d episodes", n)
	}
	openedAt := stopT0.Add(2 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, openedAt)
	if got := onlyEpisode(t, pool).openedAt; !got.Equal(openedAt) {
		t.Fatalf("opened at %v, want the next poll's time %v", got, openedAt)
	}

	h.RecordOBAMetrics(canceled, stopServer, agencies, quiet, stopT0.Add(3*time.Minute))
	if onlyEpisode(t, pool).closeReason != nil {
		t.Fatal("failed write closed the episode")
	}
	closedAt := stopT0.Add(4 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, quiet, closedAt)
	if got := onlyEpisode(t, pool).closedAt; got == nil || !got.Equal(closedAt) {
		t.Fatalf("closed at %v, want the next poll's time %v", got, closedAt)
	}
}

func TestLastReported_SavedEveryTenMinutes(t *testing.T) {
	h, pool, counter := newCountedHistory(t)
	response := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, stopT0)
	counter.reset()

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, stopT0.Add(9*time.Minute))
	if n := counter.count(""); n != 0 {
		t.Fatalf("poll before 10 minutes ran %d statements, want 0", n)
	}

	saveAt := stopT0.Add(10 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), response, saveAt)
	if n := counter.count("SaveUnmatchedStopLastReported"); n != 1 {
		t.Fatalf("save ran %d times, want 1", n)
	}
	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(saveAt) {
		t.Errorf("stored last_reported_at = %v, want %v", got, saveAt)
	}
	if n := count(t, pool, "onebusaway_metrics_response"); n != 1 {
		t.Errorf("save wrote %d responses, want only the opening one", n)
	}
}

func TestRecordOBAMetrics_ClosesOnCompleteOmission(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{}), stopT0.Add(time.Minute))

	episode := onlyEpisode(t, pool)
	if episode.closeReason == nil || *episode.closeReason != "not_reported" {
		t.Fatalf("close_reason = %v, want not_reported", episode.closeReason)
	}
	if memoryEpisode(t, h, "a", "1") != nil {
		t.Error("closed episode is still in memory")
	}
}

func TestLastReported_SavedOnClose(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, stopT0)
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, lastSeen)

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{}), stopT0.Add(2*time.Minute))

	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(lastSeen) {
		t.Errorf("closed episode last_reported_at = %v, want %v", got, lastSeen)
	}
}

func TestLastReported_SavedOnServerRemoval(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, stopT0)
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, lastSeen)

	h.FlushServer(ctx, stopServer.ObaBaseURL)

	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(lastSeen) {
		t.Errorf("stored last_reported_at = %v, want %v", got, lastSeen)
	}
	if urls := h.TrackedServerURLs(); len(urls) != 0 {
		t.Errorf("removed server still tracked: %v", urls)
	}
}

func TestLastReported_SavedOnShutdown(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, stopT0)
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, lastSeen)

	h.Flush(ctx)

	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(lastSeen) {
		t.Errorf("stored last_reported_at = %v, want %v", got, lastSeen)
	}
}

func TestRecordOBAMetrics_UnlistedAgencyClosesEpisodes(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)

	// "a" leaves agencyIDs: its responses are never complete again, so its
	// open episode closes against its last complete response, while the
	// incomplete response still opens nothing (#438).
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse(nil, map[string][]string{"a": {"2"}}), stopT0.Add(time.Minute))

	episode := onlyEpisode(t, pool)
	if episode.stopID != "1" {
		t.Fatalf("episode stop = %q, want 1: an incomplete response must not open episodes", episode.stopID)
	}
	if episode.closeReason == nil || *episode.closeReason != "agency_unlisted" {
		t.Fatalf("close_reason = %v, want agency_unlisted", episode.closeReason)
	}
	// The last complete response is the one that opened the episode.
	if episode.closedAt == nil || !episode.closedAt.Equal(stopT0) {
		t.Errorf("closed_at = %v, want the last complete response at %v", episode.closedAt, stopT0)
	}
	if memoryEpisode(t, h, "a", "1") != nil {
		t.Error("closed episode is still in memory")
	}
}

func TestRecordOBAMetrics_ServerModeOneResponse(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	response := stopsResponse([]string{"a", "b", "c"}, map[string][]string{"a": {"1"}, "b": {"2"}, "c": {"3"}})

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a", "b", "c"), response, stopT0)

	for table, want := range map[string]int{
		"onebusaway_metrics_response":        1,
		"onebusaway_metrics_response_agency": 3,
		"unmatched_stop_episode":             3,
	} {
		if got := count(t, pool, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
}

func TestRecordOBAMetrics_NullCounts(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	response := stopsResponse([]string{"a", "b"}, map[string][]string{"a": {"1"}})

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a", "b"), response, stopT0)

	rows, err := pool.Query(ctx, `
		SELECT a.external_agency_id, ra.unmatched_stop_count, ra.unmatched_trip_count,
		       ra.matched_stop_count, ra.matched_trip_count
		FROM onebusaway_metrics_response_agency ra
		JOIN agency a ON a.agency_id = ra.agency_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var agencyID string
		var unmatchedStops, unmatchedTrips, matchedStops, matchedTrips *int32
		if err := rows.Scan(&agencyID, &unmatchedStops, &unmatchedTrips, &matchedStops, &matchedTrips); err != nil {
			t.Fatal(err)
		}
		if unmatchedTrips != nil || matchedStops != nil || matchedTrips != nil {
			t.Errorf("%s: absent counts are not null", agencyID)
		}
		switch agencyID {
		case "a":
			if unmatchedStops == nil || *unmatchedStops != 1 {
				t.Errorf("a: unmatched_stop_count = %v, want 1", unmatchedStops)
			}
		case "b":
			if unmatchedStops != nil {
				t.Errorf("b: unmatched_stop_count = %v, want null", *unmatchedStops)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMetadata_Trigger1Reuse(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	withStaticVersion(t, h, pool, gtfs.NewStaticStore(), stopT0.Add(-time.Hour), testStop("1", "Main St", nil))
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	quiet := stopsResponse([]string{"a"}, map[string][]string{})

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, stopT0)
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), quiet, stopT0.Add(time.Minute))
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), reported, stopT0.Add(2*time.Minute))

	if n := count(t, pool, "unmatched_stop_episode"); n != 2 {
		t.Fatalf("got %d episodes, want 2", n)
	}
	if n := count(t, pool, "stop_metadata_version"); n != 1 {
		t.Errorf("got %d metadata rows, want the first one reused", n)
	}
	if n := count(t, pool, "unmatched_stop_episode_metadata"); n != 2 {
		t.Errorf("got %d links, want 2", n)
	}
}

func TestMetadata_Trigger2OnlyWhenChanged(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	store := gtfs.NewStaticStore()
	withStaticVersion(t, h, pool, store, stopT0.Add(-time.Hour), testStop("1", "Main St", nil))
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)

	unchanged := withStaticVersion(t, h, pool, store, stopT0.Add(time.Hour), testStop("1", "Main St", nil))
	h.onStaticVersion(ctx, stopServer, unchanged)
	if n := count(t, pool, "stop_metadata_version"); n != 1 {
		t.Fatalf("unchanged facts wrote %d metadata rows, want 1", n)
	}

	renamed := withStaticVersion(t, h, pool, store, stopT0.Add(2*time.Hour), testStop("1", "Main Street", nil))
	h.onStaticVersion(ctx, stopServer, renamed)
	if n := count(t, pool, "stop_metadata_version"); n != 2 {
		t.Fatalf("changed facts left %d metadata rows, want 2", n)
	}
	if n := count(t, pool, "unmatched_stop_episode_metadata"); n != 2 {
		t.Errorf("got %d links, want 2", n)
	}
}

func TestCluster_Assignment(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	station := remoteGtfs.Stop{Id: "S1", Name: "Central Station", Type: 1, Latitude: floatPtr(47.6), Longitude: floatPtr(-122.3)}
	withStaticVersion(t, h, pool, gtfs.NewStaticStore(), stopT0.Add(-time.Hour), testStop("1", "Platform 1", &station))
	h.EnsureClusterScheme(ctx)

	// Stop 2 is not in the snapshot: a not-found row with no assignment.
	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1", "2"}}), stopT0)

	var stationID, stationName *string
	if err := pool.QueryRow(ctx, `SELECT root_station_id, root_station_name FROM stop_cluster_assignment`).Scan(&stationID, &stationName); err != nil {
		t.Fatal(err)
	}
	if stationID == nil || *stationID != "S1" || stationName == nil || *stationName != "Central Station" {
		t.Errorf("root station = %v %v, want S1 Central Station", stationID, stationName)
	}
	for table, want := range map[string]int{
		"cluster_scheme":          1,
		"s2_cell":                 1,
		"stop_metadata_version":   2,
		"stop_cluster_assignment": 1,
	} {
		if got := count(t, pool, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
}

func TestCluster_NoStationNull(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	withStaticVersion(t, h, pool, gtfs.NewStaticStore(), stopT0.Add(-time.Hour), testStop("1", "Main St", nil))

	h.RecordOBAMetrics(ctx, stopServer, stopAgencies("a"), stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)

	var stationID, stationName *string
	if err := pool.QueryRow(ctx, `SELECT root_station_id, root_station_name FROM stop_cluster_assignment`).Scan(&stationID, &stationName); err != nil {
		t.Fatal(err)
	}
	if stationID != nil || stationName != nil {
		t.Errorf("root station = %v %v, want null", stationID, stationName)
	}
}

// TestRecordOBAMetrics_ReloadsAfterCommitReportedAsFailed covers a write that
// commits in the database while the client sees an error: memory then lacks
// an episode the database has. The next open hits the one-open-episode index
// and fails; the poll after that must reload and recover.
func TestRecordOBAMetrics_ReloadsAfterCommitReportedAsFailed(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, stopT0)

	// The commit succeeded but memory never learned of it.
	entry := h.stops.forServer(utils.SanitizeServerURL(stopServer.ObaBaseURL), stopServer)
	entry.mu.Lock()
	delete(entry.open, unmatchedStopKey{agencyID: "a", stopID: "1"})
	entry.mu.Unlock()

	// Plans an open the database already has: the write fails.
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, stopT0.Add(30*time.Second))
	if n := count(t, pool, "unmatched_stop_episode"); n != 1 {
		t.Fatalf("got %d episodes, want the original only", n)
	}

	// The next poll reloads from the database and sees the episode as open.
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, lastSeen)
	if memoryEpisode(t, h, "a", "1") == nil {
		t.Fatal("open episode not reloaded after the failed write")
	}

	// And recording works again: the omission closes it with the exact time.
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{}), stopT0.Add(2*time.Minute))
	episode := onlyEpisode(t, pool)
	if episode.closeReason == nil || *episode.closeReason != "not_reported" {
		t.Fatalf("close_reason = %v, want not_reported", episode.closeReason)
	}
	if !episode.lastReportedAt.Equal(lastSeen) {
		t.Errorf("last_reported_at = %v, want %v", episode.lastReportedAt, lastSeen)
	}
}

// TestRecordOBAMetrics_ReloadsAfterCloseReportedAsFailed is the close side:
// a close commits in the database while the client sees an error, so memory
// still holds the closed episode. The reload must drop it, so a later report
// of the stop opens a new episode.
func TestRecordOBAMetrics_ReloadsAfterCloseReportedAsFailed(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	key := unmatchedStopKey{agencyID: "a", stopID: "1"}
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, stopT0)
	closed := memoryEpisode(t, h, "a", "1")

	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{}), stopT0.Add(time.Minute))

	entry := h.stops.forServer(utils.SanitizeServerURL(stopServer.ObaBaseURL), stopServer)
	entry.mu.Lock()
	entry.open[key] = closed
	entry.loaded = false
	entry.mu.Unlock()

	reopenedAt := stopT0.Add(2 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, reopenedAt)

	episodes := storedEpisodes(t, pool)
	if len(episodes) != 2 {
		t.Fatalf("got %d episodes, want the closed one and a new one: %+v", len(episodes), episodes)
	}
	if episodes[0].closeReason == nil {
		t.Error("first episode is open, want closed")
	}
	if episodes[1].closeReason != nil || !episodes[1].openedAt.Equal(reopenedAt) {
		t.Errorf("second episode = %+v, want open at %v", episodes[1], reopenedAt)
	}
	if got := memoryEpisode(t, h, "a", "1"); got == nil || got.episodeID == closed.episodeID {
		t.Error("memory still holds the closed episode")
	}
}

// TestRecordOBAMetrics_ReloadKeepsNewerLastReported covers a failed write
// after a quiet poll moved lastReportedAt past the saved value: the reload
// must keep the newer in-memory time, and the close must write it.
func TestRecordOBAMetrics_ReloadKeepsNewerLastReported(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)
	// A quiet poll: memory moves, the database keeps stopT0.
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0.Add(time.Minute))

	// Stop "2" makes the poll write; the write fails after stop "1" was
	// marked reported in memory.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	lastSeen := stopT0.Add(2 * time.Minute)
	h.RecordOBAMetrics(canceled, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1", "2"}}), lastSeen)
	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(stopT0) {
		t.Fatalf("stored last_reported_at = %v before the reload, want %v", got, stopT0)
	}

	// The next poll reloads, then closes the episode.
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{}), stopT0.Add(3*time.Minute))

	episode := onlyEpisode(t, pool)
	if episode.closeReason == nil {
		t.Fatal("episode is open, want closed")
	}
	if !episode.lastReportedAt.Equal(lastSeen) {
		t.Errorf("closed episode last_reported_at = %v, want %v", episode.lastReportedAt, lastSeen)
	}
}

// TestRecordOBAMetrics_ReloadIgnoresOtherEpisodeTime: when memory holds a
// closed episode under the same key as a different open episode in the
// database, the reload takes the open episode's time, not the closed one's.
func TestRecordOBAMetrics_ReloadIgnoresOtherEpisodeTime(t *testing.T) {
	h, _, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	reported := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	quiet := stopsResponse([]string{"a"}, map[string][]string{})
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, stopT0)
	h.RecordOBAMetrics(ctx, stopServer, agencies, quiet, stopT0.Add(time.Minute))
	reopenedAt := stopT0.Add(2 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, reopenedAt)
	reopened := memoryEpisode(t, h, "a", "1")

	// Memory holds a stale copy of a different episode with a later time.
	entry := h.stops.forServer(utils.SanitizeServerURL(stopServer.ObaBaseURL), stopServer)
	entry.mu.Lock()
	entry.open[unmatchedStopKey{agencyID: "a", stopID: "1"}] = &openStopEpisode{
		episodeID:      reopened.episodeID - 1,
		reportedStopID: reopened.reportedStopID,
		agencyID:       reopened.agencyID,
		lastReportedAt: stopT0.Add(time.Hour),
	}
	entry.loaded = false
	entry.mu.Unlock()

	// A poll that still reports the stop reloads without closing anything.
	pollAt := stopT0.Add(3 * time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, reported, pollAt)

	got := memoryEpisode(t, h, "a", "1")
	if got == nil || got.episodeID != reopened.episodeID {
		t.Fatalf("memory episode = %+v, want episode %d", got, reopened.episodeID)
	}
	if !got.lastReportedAt.Equal(pollAt) {
		t.Errorf("lastReportedAt = %v, want the still-reported poll time %v", got.lastReportedAt, pollAt)
	}
}

// failWriteAfterStillReported opens stop 1, reports it again so memory moves
// past the stored time, then fails a write. It returns the in-memory time.
func failWriteAfterStillReported(t *testing.T, h *HistoryService) time.Time {
	t.Helper()
	agencies := stopAgencies("a")
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	// Stop 2 makes the poll write; the write fails after stop 1 was marked
	// reported in memory.
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(canceled, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1", "2"}}), lastSeen)
	return lastSeen
}

func TestLastReported_FlushAfterFailedWrite(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	lastSeen := failWriteAfterStillReported(t, h)

	h.Flush(ctx)

	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(lastSeen) {
		t.Errorf("stored last_reported_at = %v, want %v", got, lastSeen)
	}
}

func TestLastReported_FlushServerAfterFailedWrite(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	lastSeen := failWriteAfterStillReported(t, h)

	h.FlushServer(ctx, stopServer.ObaBaseURL)

	if got := onlyEpisode(t, pool).lastReportedAt; !got.Equal(lastSeen) {
		t.Errorf("stored last_reported_at = %v, want %v", got, lastSeen)
	}
}

func TestLastReported_FlushAfterFailedWriteLeavesClosedEpisode(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	agencies := stopAgencies("a")
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}}), stopT0)
	stale := memoryEpisode(t, h, "a", "1")
	closedAt := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, stopServer, agencies, stopsResponse([]string{"a"}, map[string][]string{}), closedAt)

	// A close that committed but was reported as failed: memory still holds
	// the episode, with a later time than the database.
	entry := h.stops.forServer(utils.SanitizeServerURL(stopServer.ObaBaseURL), stopServer)
	entry.mu.Lock()
	entry.open[unmatchedStopKey{agencyID: "a", stopID: "1"}] = &openStopEpisode{
		episodeID:      stale.episodeID,
		reportedStopID: stale.reportedStopID,
		agencyID:       stale.agencyID,
		lastReportedAt: stopT0.Add(time.Hour),
	}
	entry.loaded = false
	entry.mu.Unlock()

	h.Flush(ctx)

	episode := onlyEpisode(t, pool)
	if episode.closeReason == nil || !episode.lastReportedAt.Equal(stopT0) {
		t.Errorf("closed episode = %+v, want it unchanged (closed, last_reported_at %v)", episode, stopT0)
	}
}

func TestFlushAgencies_DropsOnlyRemovedAgency(t *testing.T) {
	h, pool, _ := newCountedHistory(t)
	// Two agency-mode entries share one base URL.
	agencyA, agencyB := stopAgency("a"), stopAgency("b")
	reportedA := stopsResponse([]string{"a"}, map[string][]string{"a": {"1"}})
	h.RecordOBAMetrics(ctx, agencyA, []models.ObaServer{agencyA}, reportedA, stopT0)
	h.RecordOBAMetrics(ctx, agencyB, []models.ObaServer{agencyB}, stopsResponse([]string{"b"}, map[string][]string{"b": {"2"}}), stopT0)
	lastSeen := stopT0.Add(time.Minute)
	h.RecordOBAMetrics(ctx, agencyA, []models.ObaServer{agencyA}, reportedA, lastSeen)

	// Entry "a" is removed; entry "b" stays.
	h.FlushAgencies(ctx, stopServer.ObaBaseURL, map[string]bool{"b": true})

	if memoryEpisode(t, h, "a", "1") != nil {
		t.Error("removed agency's episode is still in memory")
	}
	if memoryEpisode(t, h, "b", "2") == nil {
		t.Error("kept agency's episode was dropped")
	}
	var stored time.Time
	var closeReason *string
	if err := pool.QueryRow(ctx, `
		SELECT e.last_reported_at, e.close_reason
		FROM unmatched_stop_episode e
		JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id
		WHERE s.oba_stop_id = '1'`).Scan(&stored, &closeReason); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(lastSeen) {
		t.Errorf("removed agency's last_reported_at = %v, want %v saved", stored, lastSeen)
	}
	if closeReason == nil || *closeReason != "agency_removed" {
		t.Errorf("removed agency's close_reason = %v, want agency_removed", closeReason)
	}
	var keptReason *string
	if err := pool.QueryRow(ctx, `
		SELECT e.close_reason
		FROM unmatched_stop_episode e
		JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id
		WHERE s.oba_stop_id = '2'`).Scan(&keptReason); err != nil {
		t.Fatal(err)
	}
	if keptReason != nil {
		t.Errorf("kept agency's close_reason = %v, want still open", *keptReason)
	}

	// After a restart, the removed agency's episode stays out of memory: a
	// fresh service over the same database loads only open rows (#438).
	h2 := NewHistoryService(pool, discardLogger(), "test-build", nil)
	h2.RecordOBAMetrics(ctx, agencyB, []models.ObaServer{agencyB}, stopsResponse([]string{"b"}, map[string][]string{"b": {"2"}}), stopT0.Add(2*time.Minute))
	if memoryEpisode(t, h2, "a", "1") != nil {
		t.Error("removed agency's closed episode was reloaded after restart")
	}
	if memoryEpisode(t, h2, "b", "2") == nil {
		t.Error("kept agency's episode was not reloaded after restart")
	}
}
