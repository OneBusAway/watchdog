package history

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

const testFeedURL = "https://feeds.test-transit.example/gtfs.zip"

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// testPublication is a server-scoped publication of two agencies from one
// feed whose artifact hashes to hashOf(content). It carries a box for agency
// 1 and the server-wide box; agency 2 has none, so its key must be skipped.
func testPublication(content string, publishedAt time.Time) gtfs.StaticVersionPublication {
	return gtfs.StaticVersionPublication{
		Server:      testServer,
		PublishedAt: publishedAt,
		Feeds:       []gtfs.StaticFeedContent{{URL: testFeedURL, ContentSHA256: hashOf(content)}},
		Agencies: []gtfs.StaticPublishedAgency{
			{AgencyID: "1", AgencyName: "Agency One"},
			{AgencyID: "2", AgencyName: "Agency Two"},
		},
		BoundingBoxes: map[string]geo.BoundingBox{
			models.ServerKey(testServer.ObaBaseURL, "1"): {MinLat: 47.0, MaxLat: 47.5, MinLon: -122.5, MaxLon: -122.0},
			models.ServerKey(testServer.ObaBaseURL, ""):  {MinLat: 46.5, MaxLat: 48.0, MinLon: -123.0, MaxLon: -121.5},
		},
	}
}

// agencyServer is an agency-scoped entry on testServer's base URL.
func agencyServer(agencyID string) models.ObaServer {
	server := testServer
	server.AgencyID = agencyID
	server.AgencyName = "Agency " + agencyID
	return server
}

// agencyPublication is an agency-scoped publication with the agency's own
// box, as storeStaticSnapshot builds it.
func agencyPublication(agencyID, content string, publishedAt time.Time, lat float64) gtfs.StaticVersionPublication {
	server := agencyServer(agencyID)
	return gtfs.StaticVersionPublication{
		Server:      server,
		PublishedAt: publishedAt,
		Feeds:       []gtfs.StaticFeedContent{{URL: testFeedURL, ContentSHA256: hashOf(content)}},
		Agencies:    []gtfs.StaticPublishedAgency{{AgencyID: agencyID, AgencyName: server.AgencyName}},
		BoundingBoxes: map[string]geo.BoundingBox{
			server.ServerKey(): {MinLat: lat, MaxLat: lat + 0.5, MinLon: -122.5, MaxLon: -122.0},
		},
	}
}

// versionAgencies returns the external agency IDs recorded with a version.
func versionAgencies(t *testing.T, pool *pgxpool.Pool, versionID int64) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT a.external_agency_id
		FROM gtfs_static_version_agency va
		JOIN agency a ON a.agency_id = va.agency_id
		WHERE va.gtfs_static_version_id = $1
		ORDER BY a.external_agency_id`, versionID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func latestVersionID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, "SELECT max(gtfs_static_version_id) FROM gtfs_static_version").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStaticVersion_Recorded(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	publishedAt := time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC)

	h.StaticVersionObserver()(testPublication("feed-v1", publishedAt))

	for table, want := range map[string]int{
		"gtfs_static_version":        1,
		"static_gtfs_source":         1,
		"gtfs_static_version_source": 1,
		"gtfs_static_version_agency": 2,
		"bounding_box_version":       2,
	} {
		if got := count(t, pool, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}

	versionID := latestVersionID(t, pool)
	var gotPublishedAt time.Time
	var combined, parser string
	if err := pool.QueryRow(ctx,
		"SELECT published_at, combined_content_sha256, parser_version FROM gtfs_static_version WHERE gtfs_static_version_id = $1",
		versionID).Scan(&gotPublishedAt, &combined, &parser); err != nil {
		t.Fatal(err)
	}
	if !gotPublishedAt.Equal(publishedAt) {
		t.Errorf("published_at = %v, want %v", gotPublishedAt, publishedAt)
	}
	if want := combinedContentSHA256([]string{hashOf("feed-v1")}); combined != want {
		t.Errorf("combined_content_sha256 = %q, want %q", combined, want)
	}
	if parser != "test-build" {
		t.Errorf("parser_version = %q, want %q", parser, "test-build")
	}

	var sourceURL, sourceHash string
	if err := pool.QueryRow(ctx, `
		SELECT s.normalized_source_url, vs.source_content_sha256
		FROM gtfs_static_version_source vs
		JOIN static_gtfs_source s ON s.source_id = vs.source_id
		WHERE vs.gtfs_static_version_id = $1`, versionID).Scan(&sourceURL, &sourceHash); err != nil {
		t.Fatal(err)
	}
	if sourceHash != hashOf("feed-v1") {
		t.Errorf("source_content_sha256 = %q, want %q", sourceHash, hashOf("feed-v1"))
	}
	if sourceURL == "" {
		t.Error("source URL is empty")
	}

	var agencyBoxes, serverBoxes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE agency_id IS NOT NULL), count(*) FILTER (WHERE agency_id IS NULL)
		FROM bounding_box_version WHERE gtfs_static_version_id = $1`, versionID).Scan(&agencyBoxes, &serverBoxes); err != nil {
		t.Fatal(err)
	}
	if agencyBoxes != 1 || serverBoxes != 1 {
		t.Errorf("boxes: %d agency, %d server-wide; want 1 and 1", agencyBoxes, serverBoxes)
	}

	if got, ok := h.CurrentStaticVersion(testServer.ServerKey()); !ok || got != versionID {
		t.Errorf("CurrentStaticVersion = %d, %v; want %d, true", got, ok, versionID)
	}
	if _, ok := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1")); !ok {
		t.Error("agency 1 has no current bounding box")
	}
	if _, ok := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "")); !ok {
		t.Error("server-wide box is not current")
	}
	if _, ok := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "2")); ok {
		t.Error("agency 2 has a current box although the store held none")
	}
}

// A restart with unchanged feeds publishes the same hashes again. It records
// nothing new and makes the existing version and its boxes current, even for
// a service that has no memory of them.
func TestStaticVersion_UnchangedNotDuplicated(t *testing.T) {
	pool := newMigratedPool(t)
	first := NewHistoryService(pool, discardLogger(), "test-build", nil)
	first.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	versionID := latestVersionID(t, pool)
	wantBox, _ := first.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1"))

	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	restarted.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC().Add(time.Hour)))

	for table, want := range map[string]int{
		"gtfs_static_version":        1,
		"gtfs_static_version_source": 1,
		"bounding_box_version":       2,
	} {
		if got := count(t, pool, table); got != want {
			t.Errorf("%s has %d rows after an unchanged publication, want %d", table, got, want)
		}
	}
	if got, ok := restarted.CurrentStaticVersion(testServer.ServerKey()); !ok || got != versionID {
		t.Errorf("CurrentStaticVersion = %d, %v; want the existing %d", got, ok, versionID)
	}
	if got, ok := restarted.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1")); !ok || got != wantBox {
		t.Errorf("CurrentBoundingBox = %d, %v; want the existing %d", got, ok, wantBox)
	}
}

// The same bytes from a new feed URL reuse the version and link that URL, so
// history names the source that is configured now. The original link stays.
func TestStaticVersion_NewURLLinksReusedVersion(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	now := time.Now().UTC()
	h.StaticVersionObserver()(testPublication("feed-v1", now))
	versionID := latestVersionID(t, pool)

	again := testPublication("feed-v1", now.Add(time.Hour))
	const movedURL = "https://feeds.test-transit.example/moved.zip"
	again.Feeds[0].URL = movedURL
	h.StaticVersionObserver()(again)

	if got := count(t, pool, "gtfs_static_version"); got != 1 {
		t.Fatalf("gtfs_static_version has %d rows, want the reused version", got)
	}
	if got := count(t, pool, "static_gtfs_source"); got != 2 {
		t.Errorf("static_gtfs_source has %d rows, want the old URL and the new one", got)
	}
	if got := count(t, pool, "gtfs_static_version_source"); got != 2 {
		t.Errorf("gtfs_static_version_source has %d rows, want both URLs on the reused version", got)
	}
	var linked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM gtfs_static_version_source vs
		JOIN static_gtfs_source s ON s.source_id = vs.source_id
		WHERE vs.gtfs_static_version_id = $1
		  AND s.normalized_source_url = $2
		  AND vs.source_content_sha256 = $3`,
		versionID, movedURL, hashOf("feed-v1")).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 1 {
		t.Errorf("new URL linked %d times with the feed hash, want 1", linked)
	}
	if got, ok := h.CurrentStaticVersion(testServer.ServerKey()); !ok || got != versionID {
		t.Errorf("CurrentStaticVersion = %d, %v; want reused %d", got, ok, versionID)
	}
}

func TestStaticVersion_ChangedSource(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	h.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	firstID := latestVersionID(t, pool)
	firstBox, _ := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1"))

	h.StaticVersionObserver()(testPublication("feed-v2", time.Now().UTC().Add(time.Minute)))

	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Fatalf("gtfs_static_version has %d rows, want 2", got)
	}
	// The source URL is unchanged, so it stays one identity.
	if got := count(t, pool, "static_gtfs_source"); got != 1 {
		t.Errorf("static_gtfs_source has %d rows, want 1", got)
	}
	secondID := latestVersionID(t, pool)
	if secondID == firstID {
		t.Fatal("no new version was recorded")
	}
	var hash string
	if err := pool.QueryRow(ctx, "SELECT source_content_sha256 FROM gtfs_static_version_source WHERE gtfs_static_version_id = $1", secondID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != hashOf("feed-v2") {
		t.Errorf("new version's source hash = %q, want %q", hash, hashOf("feed-v2"))
	}
	if got, _ := h.CurrentStaticVersion(testServer.ServerKey()); got != secondID {
		t.Errorf("CurrentStaticVersion = %d, want %d", got, secondID)
	}
	if got, _ := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1")); got == firstBox {
		t.Error("the current box still belongs to the old version")
	}
}

// A failed write leaves nothing current, so new history cannot point at a
// version Watchdog no longer uses.
func TestStaticVersion_FailedWriteClearsCurrent(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	h.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	if _, ok := h.CurrentStaticVersion(testServer.ServerKey()); !ok {
		t.Fatal("setup: no current version")
	}

	// A latitude outside [-90, 90] fails the schema's CHECK constraint.
	failing := testPublication("feed-v2", time.Now().UTC())
	failing.BoundingBoxes[models.ServerKey(testServer.ObaBaseURL, "1")] = geo.BoundingBox{MinLat: 0, MaxLat: 123.4, MinLon: 0, MaxLon: 1}
	h.StaticVersionObserver()(failing)

	if got := count(t, pool, "gtfs_static_version"); got != 1 {
		t.Errorf("gtfs_static_version has %d rows, want the failed version rolled back", got)
	}
	if _, ok := h.CurrentStaticVersion(testServer.ServerKey()); ok {
		t.Error("a version is still current after a failed write")
	}
	if _, ok := h.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "")); ok {
		t.Error("a box is still current after a failed write")
	}
}

func TestStaticVersion_CombinedHash(t *testing.T) {
	a, b := hashOf("a"), hashOf("b")
	sum := sha256.Sum256([]byte(a + "\n" + b))
	if got, want := combinedContentSHA256([]string{a, b}), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("combinedContentSHA256 = %q, want %q", got, want)
	}
	if combinedContentSHA256([]string{a, b}) == combinedContentSHA256([]string{b, a}) {
		t.Fatal("combined hash ignores configured order")
	}
	single := sha256.Sum256([]byte(a))
	if got := combinedContentSHA256([]string{a}); got != hex.EncodeToString(single[:]) {
		t.Fatalf("one feed: got %q, want the hash of its hash", got)
	}
}

func TestStaticVersion_NilPoolNoop(t *testing.T) {
	h := NewHistoryService(nil, discardLogger(), "test-build", nil)
	h.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	if _, ok := h.CurrentStaticVersion(testServer.ServerKey()); ok {
		t.Fatal("a version became current with storage disabled")
	}
}

// Two agency-scoped entries on one base URL share a database server. When
// they publish the same feed, each records its own version with its own
// agency and box, and neither disturbs the other's current box. A restart
// reuses each entry's own version.
func TestStaticVersion_AgencyEntriesSharedHash(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	now := time.Now().UTC()
	one, forty := agencyServer("1"), agencyServer("40")

	h.StaticVersionObserver()(agencyPublication("1", "shared-feed", now, 47.0))
	h.StaticVersionObserver()(agencyPublication("40", "shared-feed", now.Add(time.Second), 45.0))

	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Fatalf("gtfs_static_version has %d rows, want one per agency entry", got)
	}
	versionOne, okOne := h.CurrentStaticVersion(one.ServerKey())
	versionForty, okForty := h.CurrentStaticVersion(forty.ServerKey())
	if !okOne || !okForty || versionOne == versionForty {
		t.Fatalf("current versions = %d (%v), %d (%v); want two different versions", versionOne, okOne, versionForty, okForty)
	}
	if got := versionAgencies(t, pool, versionOne); len(got) != 1 || got[0] != "1" {
		t.Errorf("agency 1's version lists agencies %v, want [1]", got)
	}
	if got := versionAgencies(t, pool, versionForty); len(got) != 1 || got[0] != "40" {
		t.Errorf("agency 40's version lists agencies %v, want [40]", got)
	}
	boxOne, okOne := h.CurrentBoundingBox(one.ServerKey())
	boxForty, okForty := h.CurrentBoundingBox(forty.ServerKey())
	if !okOne || !okForty || boxOne == boxForty {
		t.Fatalf("current boxes = %d (%v), %d (%v); want each agency's own box", boxOne, okOne, boxForty, okForty)
	}

	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	restarted.StaticVersionObserver()(agencyPublication("40", "shared-feed", now.Add(time.Hour), 45.0))
	restarted.StaticVersionObserver()(agencyPublication("1", "shared-feed", now.Add(time.Hour), 47.0))

	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Errorf("gtfs_static_version has %d rows after a restart, want 2", got)
	}
	if got, _ := restarted.CurrentStaticVersion(one.ServerKey()); got != versionOne {
		t.Errorf("agency 1 reused version %d, want its own %d", got, versionOne)
	}
	if got, _ := restarted.CurrentStaticVersion(forty.ServerKey()); got != versionForty {
		t.Errorf("agency 40 reused version %d, want its own %d", got, versionForty)
	}
	if got, ok := restarted.CurrentBoundingBox(one.ServerKey()); !ok || got != boxOne {
		t.Errorf("agency 1's box = %d (%v), want %d", got, ok, boxOne)
	}
	if got, ok := restarted.CurrentBoundingBox(forty.ServerKey()); !ok || got != boxForty {
		t.Errorf("agency 40's box = %d (%v), want %d", got, ok, boxForty)
	}
}

// With different feeds, the two entries must not take turns being the
// server's newest version: a restart records nothing new.
func TestStaticVersion_AgencyEntriesDifferentHashes(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	now := time.Now().UTC()

	h.StaticVersionObserver()(agencyPublication("1", "feed-one", now, 47.0))
	h.StaticVersionObserver()(agencyPublication("40", "feed-forty", now.Add(time.Second), 45.0))
	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Fatalf("setup: gtfs_static_version has %d rows, want 2", got)
	}

	for restart := 1; restart <= 2; restart++ {
		restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
		at := now.Add(time.Duration(restart) * time.Hour)
		restarted.StaticVersionObserver()(agencyPublication("1", "feed-one", at, 47.0))
		restarted.StaticVersionObserver()(agencyPublication("40", "feed-forty", at, 45.0))
		if got := count(t, pool, "gtfs_static_version"); got != 2 {
			t.Fatalf("restart %d: gtfs_static_version has %d rows, want 2", restart, got)
		}
	}
}

// serverPublication is a server-scoped publication of content that declares
// agencyIDs, with no bounding boxes.
func serverPublication(content string, publishedAt time.Time, agencyIDs ...string) gtfs.StaticVersionPublication {
	agencies := make([]gtfs.StaticPublishedAgency, len(agencyIDs))
	for i, id := range agencyIDs {
		agencies[i] = gtfs.StaticPublishedAgency{AgencyID: id, AgencyName: "Agency " + id}
	}
	return gtfs.StaticVersionPublication{
		Server:      testServer,
		PublishedAt: publishedAt,
		Feeds:       []gtfs.StaticFeedContent{{URL: testFeedURL, ContentSHA256: hashOf(content)}},
		Agencies:    agencies,
	}
}

// A server-scoped entry reuses a version only when it is the newest
// server-scoped one. A publication that returns to an earlier feed and
// agency set is a rollback, so it gets a new version with its own
// published_at; otherwise history could not show that the feed went back.
func TestStaticVersion_ServerScopedReuse(t *testing.T) {
	type step struct {
		content  string
		agencies []string
	}
	h1A := step{"feed-h1", []string{"A"}}
	h2A := step{"feed-h2", []string{"A"}}
	h2AB := step{"feed-h2", []string{"A", "B"}}

	tests := []struct {
		name         string
		steps        []step
		wantVersions int
		// wantLastNew is whether the last publication recorded a new
		// version rather than reusing an earlier one.
		wantLastNew bool
	}{
		{"unchanged restart reuses", []step{h1A, h1A}, 1, false},
		{"unchanged after agency change reuses", []step{h1A, h2AB, h2AB}, 2, false},
		{"rollback after content change", []step{h1A, h2A, h1A}, 3, true},
		{"rollback after agency set change", []step{h1A, h2AB, h1A}, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newMigratedPool(t)
			h := NewHistoryService(pool, discardLogger(), "test-build", nil)
			start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)

			var last time.Time
			for i, s := range tt.steps {
				last = start.Add(time.Duration(i) * time.Hour)
				h.StaticVersionObserver()(serverPublication(s.content, last, s.agencies...))
			}

			if got := count(t, pool, "gtfs_static_version"); got != tt.wantVersions {
				t.Errorf("gtfs_static_version has %d rows, want %d", got, tt.wantVersions)
			}
			current, ok := h.CurrentStaticVersion(testServer.ServerKey())
			if !ok {
				t.Fatal("no current static version")
			}
			if newest := latestVersionID(t, pool); current != newest {
				t.Errorf("CurrentStaticVersion = %d, want the newest version %d", current, newest)
			}
			var publishedAt time.Time
			if err := pool.QueryRow(ctx,
				"SELECT published_at FROM gtfs_static_version WHERE gtfs_static_version_id = $1",
				current).Scan(&publishedAt); err != nil {
				t.Fatal(err)
			}
			if gotNew := publishedAt.Equal(last); gotNew != tt.wantLastNew {
				t.Errorf("current version published_at = %v; last publication at %v recorded a new version = %v, want %v",
					publishedAt, last, gotNew, tt.wantLastNew)
			}
		})
	}
}

// A slow write for an older publication must not replace the version a
// newer publication already made current.
func TestStaticVersion_OlderPublicationDoesNotReplaceNewer(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	now := time.Now().UTC()

	h.StaticVersionObserver()(testPublication("feed-v2", now))
	newer, _ := h.CurrentStaticVersion(testServer.ServerKey())
	h.StaticVersionObserver()(testPublication("feed-v1", now.Add(-time.Minute)))

	if got, _ := h.CurrentStaticVersion(testServer.ServerKey()); got != newer {
		t.Errorf("CurrentStaticVersion = %d, want the newer publication's %d", got, newer)
	}
}

// An agency-scoped publication on the same base URL is not the server
// entry's newest version. An unchanged server restart reuses the server
// version. A later change of the server feed still records a new one.
func TestStaticVersion_ServerScopeIgnoresAgencyEntries(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil)
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	agency := agencyServer("A")

	h.StaticVersionObserver()(serverPublication("server-feed", now, "A", "B"))
	serverVersion, ok := h.CurrentStaticVersion(testServer.ServerKey())
	if !ok {
		t.Fatal("no server version")
	}
	h.StaticVersionObserver()(agencyPublication("A", "agency-feed", now.Add(time.Minute), 47))
	agencyVersion, ok := h.CurrentStaticVersion(agency.ServerKey())
	if !ok {
		t.Fatal("no agency version")
	}
	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Fatalf("setup: gtfs_static_version has %d rows, want 2", got)
	}
	assertServerScoped := func(versionID int64, want bool) {
		t.Helper()
		var scoped bool
		if err := pool.QueryRow(ctx, "SELECT server_scoped FROM gtfs_static_version WHERE gtfs_static_version_id = $1", versionID).Scan(&scoped); err != nil {
			t.Fatal(err)
		}
		if scoped != want {
			t.Errorf("version %d server_scoped = %v, want %v", versionID, scoped, want)
		}
	}
	assertServerScoped(serverVersion, true)
	assertServerScoped(agencyVersion, false)

	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil)
	restarted.StaticVersionObserver()(serverPublication("server-feed", now.Add(time.Hour), "A", "B"))
	if got := count(t, pool, "gtfs_static_version"); got != 2 {
		t.Fatalf("unchanged server restart has %d versions, want 2", got)
	}
	if got, _ := restarted.CurrentStaticVersion(testServer.ServerKey()); got != serverVersion {
		t.Errorf("server reused version %d, want %d", got, serverVersion)
	}

	restarted.StaticVersionObserver()(serverPublication("server-feed-v2", now.Add(2*time.Hour), "A"))
	if got := count(t, pool, "gtfs_static_version"); got != 3 {
		t.Fatalf("server feed change has %d versions, want 3", got)
	}
	current, _ := restarted.CurrentStaticVersion(testServer.ServerKey())
	if current == serverVersion {
		t.Fatal("changed server feed reused the old version")
	}
	if got := versionAgencies(t, pool, current); len(got) != 1 || got[0] != "A" {
		t.Errorf("new server version agencies = %v, want [A]", got)
	}
	if got, _ := restarted.CurrentStaticVersion(agency.ServerKey()); got != agencyVersion {
		t.Errorf("agency current version = %d, want its own %d", got, agencyVersion)
	}
}
