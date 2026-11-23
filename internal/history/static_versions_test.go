package history

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

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
// feed whose artifact hashes to hashOf(content).
func testPublication(content string, publishedAt time.Time) gtfs.StaticVersionPublication {
	return gtfs.StaticVersionPublication{
		Server:      testServer,
		PublishedAt: publishedAt,
		Feeds:       []gtfs.StaticFeedContent{{URL: testFeedURL, ContentSHA256: hashOf(content)}},
		Agencies: []gtfs.StaticPublishedAgency{
			{AgencyID: "1", AgencyName: "Agency One"},
			{AgencyID: "2", AgencyName: "Agency Two"},
		},
	}
}

// testBoxes holds a box for agency 1 and the server-wide box; agency 2 has
// none, so its key must be skipped.
func testBoxes() *geo.BoundingBoxStore {
	boxes := geo.NewBoundingBoxStore()
	boxes.Set(models.ServerKey(testServer.ObaBaseURL, "1"), geo.BoundingBox{MinLat: 47.0, MaxLat: 47.5, MinLon: -122.5, MaxLon: -122.0})
	boxes.Set(models.ServerKey(testServer.ObaBaseURL, ""), geo.BoundingBox{MinLat: 46.5, MaxLat: 48.0, MinLon: -123.0, MaxLon: -121.5})
	return boxes
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
	h := NewHistoryService(pool, discardLogger(), "test-build", nil, testBoxes())
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
	first := NewHistoryService(pool, discardLogger(), "test-build", nil, testBoxes())
	first.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	versionID := latestVersionID(t, pool)
	wantBox, _ := first.CurrentBoundingBox(models.ServerKey(testServer.ObaBaseURL, "1"))

	restarted := NewHistoryService(pool, discardLogger(), "test-build", nil, testBoxes())
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

func TestStaticVersion_ChangedSource(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test-build", nil, testBoxes())
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
	boxes := testBoxes()
	h := NewHistoryService(pool, discardLogger(), "test-build", nil, boxes)
	h.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	if _, ok := h.CurrentStaticVersion(testServer.ServerKey()); !ok {
		t.Fatal("setup: no current version")
	}

	// A latitude outside [-90, 90] fails the schema's CHECK constraint.
	boxes.Set(models.ServerKey(testServer.ObaBaseURL, "1"), geo.BoundingBox{MinLat: 0, MaxLat: 123.4, MinLon: 0, MaxLon: 1})
	h.StaticVersionObserver()(testPublication("feed-v2", time.Now().UTC()))

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
	h := NewHistoryService(nil, discardLogger(), "test-build", nil, testBoxes())
	h.StaticVersionObserver()(testPublication("feed-v1", time.Now().UTC()))
	if _, ok := h.CurrentStaticVersion(testServer.ServerKey()); ok {
		t.Fatal("a version became current with storage disabled")
	}
}
