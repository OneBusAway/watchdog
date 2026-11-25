package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// staticVersionStep names static version recording in logs, Sentry and the
// write failure counter.
const staticVersionStep = "static_version"

// currentStatic remembers, per configured server entry, the static version
// Watchdog is using and the bounding boxes recorded with it. Stop lookups and
// vehicle evidence point at these IDs.
type currentStatic struct {
	mu sync.RWMutex
	// versions maps a server key (models.ObaServer.ServerKey) to its current
	// gtfs_static_version row and the snapshots it was recorded from.
	versions map[string]staticVersion
	// boxes maps a bounding box key (the geo.BoundingBoxStore key) to the
	// ID of its current bounding_box_version row.
	boxes map[string]int64
	// boxKeys maps a server key to the box keys its current version set, so
	// the next version replaces exactly those.
	boxKeys map[string][]string
	// publishedAt maps a server key to the newest publication applied, so a
	// slow write for an older publication cannot replace a newer one.
	publishedAt map[string]time.Time
}

func newCurrentStatic() *currentStatic {
	return &currentStatic{
		versions:    make(map[string]staticVersion),
		boxes:       make(map[string]int64),
		boxKeys:     make(map[string][]string),
		publishedAt: make(map[string]time.Time),
	}
}

// staticVersion is a recorded version and the snapshots it was recorded from,
// keyed like gtfs.StaticStore.
type staticVersion struct {
	id        int64
	snapshots map[string]*models.StaticData
}

func (c *currentStatic) currentVersion(serverKey string) (staticVersion, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	version, ok := c.versions[serverKey]
	return version, ok
}

func (c *currentStatic) box(boxKey string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.boxes[boxKey]
	return id, ok
}

// set makes versionID and boxes current for serverKey, dropping the boxes
// its previous version set. It reports false and changes nothing when a newer
// publication was already applied.
func (c *currentStatic) set(serverKey string, publishedAt time.Time, version staticVersion, boxes map[string]int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.applyLocked(serverKey, publishedAt) {
		return false
	}
	c.dropLocked(serverKey)
	c.versions[serverKey] = version
	keys := make([]string, 0, len(boxes))
	for key, id := range boxes {
		c.boxes[key] = id
		keys = append(keys, key)
	}
	c.boxKeys[serverKey] = keys
	return true
}

// forget drops serverKey's current version and boxes, unless a newer
// publication was already applied.
func (c *currentStatic) forget(serverKey string, publishedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.applyLocked(serverKey, publishedAt) {
		c.dropLocked(serverKey)
	}
}

// applyLocked records publishedAt as serverKey's newest publication, or
// reports false when it is older than one already applied.
func (c *currentStatic) applyLocked(serverKey string, publishedAt time.Time) bool {
	if newest, ok := c.publishedAt[serverKey]; ok && publishedAt.Before(newest) {
		return false
	}
	c.publishedAt[serverKey] = publishedAt
	return true
}

func (c *currentStatic) dropLocked(serverKey string) {
	for _, key := range c.boxKeys[serverKey] {
		delete(c.boxes, key)
	}
	delete(c.boxKeys, serverKey)
	delete(c.versions, serverKey)
}

// StaticVersionObserver returns the function that records a static GTFS
// publication. app.New registers it with GtfsService.SetStaticVersionObserver.
func (h *HistoryService) StaticVersionObserver() gtfs.StaticVersionObserver {
	return func(publication gtfs.StaticVersionPublication) {
		h.recordStaticVersion(context.Background(), publication)
	}
}

// CurrentStaticVersion returns the ID of the static version Watchdog is
// currently using for a server key (models.ObaServer.ServerKey), if any.
func (h *HistoryService) CurrentStaticVersion(serverKey string) (int64, bool) {
	if h == nil || h.static == nil {
		return 0, false
	}
	version, ok := h.static.currentVersion(serverKey)
	return version.id, ok
}

// CurrentBoundingBox returns the ID of the current bounding box for a bounding
// box key (models.ServerKey(baseURL, agencyID), or an empty agency ID for the
// server-wide box), if any.
func (h *HistoryService) CurrentBoundingBox(boxKey string) (int64, bool) {
	if h == nil || h.static == nil {
		return 0, false
	}
	return h.static.box(boxKey)
}

// onStaticVersion is called after a publication is recorded and committed and
// its version made current, whether the version is new or reused. It writes
// stop metadata for the server's open unmatched-stop episodes whose static
// facts changed (trigger 2). Unmatched trip recording
// (#157) hooks in here too.
func (h *HistoryService) onStaticVersion(ctx context.Context, server models.ObaServer, versionID int64) {
	h.recordStopMetadataForVersion(ctx, server, versionID)
}

// recordStaticVersion records one publication in a single transaction and,
// once it commits, makes the version and its boxes current.
func (h *HistoryService) recordStaticVersion(ctx context.Context, publication gtfs.StaticVersionPublication) {
	if h == nil || h.pool == nil {
		return
	}
	// write bounds the transaction, but the queries run on the context fn
	// captures, so bound that too.
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	serverKey := publication.Server.ServerKey()
	// Read before the write: this is the snapshot the publication installed.
	snapshots := h.publishedSnapshots(publication)
	var recorded staticVersionRecord
	committed := h.write(ctx, staticVersionStep, publicationTags(publication.Server), func(q *dbgen.Queries) error {
		var err error
		recorded, err = recordStaticVersion(ctx, q, publication, h.resolver, h.version)
		return err
	})
	if !committed {
		// The snapshot in use is no longer the recorded current version.
		// Pointing new history at the old one would be wrong, so point at
		// none until a publication records successfully.
		h.static.forget(serverKey, publication.PublishedAt)
		return
	}
	version := staticVersion{id: recorded.versionID, snapshots: snapshots}
	if h.static.set(serverKey, publication.PublishedAt, version, recorded.boxes) {
		// Also for a reused version: an episode opened before it was current
		// has no metadata yet. Unchanged facts write nothing. The trigger
		// bounds itself, so it does not share the timeout above.
		h.onStaticVersion(context.WithoutCancel(ctx), publication.Server, recorded.versionID)
	}
}

// publishedSnapshots returns the snapshots a publication installed, keyed
// like gtfs.StaticStore.
func (h *HistoryService) publishedSnapshots(publication gtfs.StaticVersionPublication) map[string]*models.StaticData {
	server := publication.Server
	keys := []string{server.ServerKey()}
	for _, agency := range publication.Agencies {
		keys = append(keys, models.ServerKey(server.ObaBaseURL, agency.AgencyID))
	}
	snapshots := make(map[string]*models.StaticData, len(keys))
	for _, key := range keys {
		if data := staticSnapshot(h.staticStore, key); data != nil {
			snapshots[key] = data
		}
	}
	return snapshots
}

// publicationTags tags a publication's write failure with the agency when
// the server entry is agency-scoped, since the publication then runs per
// agency.
func publicationTags(server models.ObaServer) writeTags {
	if server.IsServerScoped() {
		return serverTags(server)
	}
	return agencyTags(server, server.AgencyID, server.AgencyName)
}

// staticVersionRecord is the outcome of recording one publication.
type staticVersionRecord struct {
	versionID int64
	// created is false when an existing version was reused (see
	// reusableStaticVersion).
	created bool
	// boxes maps each bounding box key to the ID recorded for it.
	boxes map[string]int64
}

// recordStaticVersion writes one publication with q:
//
//  1. Ensure the server and each static source.
//  2. Compute the combined content hash.
//  3. If the newest version in the entry's scope has the same agencies and
//     the same combined hash, reuse it (a restart with unchanged feeds). See
//     reusableStaticVersion for each entry's scope.
//  4. Otherwise insert the version, one source row per feed and one agency
//     row per published agency.
//  5. Insert the publication's bounding boxes for each published agency and
//     the server-wide box, skipping keys without a box.
func recordStaticVersion(ctx context.Context, q *dbgen.Queries, publication gtfs.StaticVersionPublication, resolver *ServerNameResolver, parserVersion string) (staticVersionRecord, error) {
	server := publication.Server
	if len(publication.Feeds) == 0 {
		return staticVersionRecord{}, fmt.Errorf("server %q: static publication has no feeds", server.ServerName)
	}

	serverID, err := ensureServer(ctx, q, server, resolver)
	if err != nil {
		return staticVersionRecord{}, err
	}

	sourceIDs := make([]int64, len(publication.Feeds))
	hashes := make([]string, len(publication.Feeds))
	for i, feed := range publication.Feeds {
		if !isSHA256Hex(feed.ContentSHA256) {
			// The error names the feed by position: a URL may carry
			// credentials.
			return staticVersionRecord{}, fmt.Errorf("server %q: static feed %d has no valid content hash", server.ServerName, i+1)
		}
		sourceIDs[i], err = ensureStaticSource(ctx, q, serverID, server.ServerName, feed.URL)
		if err != nil {
			return staticVersionRecord{}, err
		}
		hashes[i] = feed.ContentSHA256
	}
	combined := combinedContentSHA256(hashes)

	agencies := uniquePublishedAgencies(publication.Agencies)
	externalAgencyIDs := make([]string, len(agencies))
	for i, agency := range agencies {
		externalAgencyIDs[i] = agency.AgencyID
	}
	reuseID, reuse, err := reusableStaticVersion(ctx, q, server, serverID, externalAgencyIDs, combined)
	if err != nil {
		return staticVersionRecord{}, err
	}
	if reuse {
		recordedBoxes, err := recordedBoundingBoxes(ctx, q, server, externalAgencyIDs, reuseID)
		if err != nil {
			return staticVersionRecord{}, err
		}
		return staticVersionRecord{versionID: reuseID, boxes: recordedBoxes}, nil
	}

	var parser *string
	if parserVersion != "" {
		parser = &parserVersion
	}
	versionID, err := q.InsertGTFSStaticVersion(ctx, dbgen.InsertGTFSStaticVersionParams{
		ServerID:              serverID,
		PublishedAt:           publication.PublishedAt.UTC(),
		CombinedContentSha256: &combined,
		ParserVersion:         parser,
	})
	if err != nil {
		return staticVersionRecord{}, fmt.Errorf("server %q: insert static version: %w", server.ServerName, err)
	}

	// Two configured URLs that normalize to the same source are one source
	// row; the first one's hash is kept.
	seenSources := make(map[int64]bool, len(sourceIDs))
	for i, sourceID := range sourceIDs {
		if seenSources[sourceID] {
			continue
		}
		seenSources[sourceID] = true
		if err := q.InsertGTFSStaticVersionSource(ctx, dbgen.InsertGTFSStaticVersionSourceParams{
			GtfsStaticVersionID: versionID,
			SourceID:            sourceID,
			SourceContentSha256: hashes[i],
		}); err != nil {
			return staticVersionRecord{}, fmt.Errorf("server %q: insert static version source %d: %w", server.ServerName, i+1, err)
		}
	}

	agencyIDs := make(map[string]int64, len(agencies))
	for _, agency := range agencies {
		agencyID, err := ensureAgency(ctx, q, serverID, server.ServerName, agency.AgencyID, agency.AgencyName)
		if err != nil {
			return staticVersionRecord{}, err
		}
		agencyIDs[agency.AgencyID] = agencyID
		if err := q.InsertGTFSStaticVersionAgency(ctx, dbgen.InsertGTFSStaticVersionAgencyParams{
			GtfsStaticVersionID: versionID,
			AgencyID:            agencyID,
		}); err != nil {
			return staticVersionRecord{}, fmt.Errorf("server %q: insert static version agency %q: %w", server.ServerName, agency.AgencyID, err)
		}
	}

	recordedBoxes := make(map[string]int64)
	insertBox := func(boxKey string, agencyID *int64, label string) error {
		bbox, ok := publication.BoundingBoxes[boxKey]
		if !ok {
			return nil
		}
		boxID, err := q.InsertBoundingBoxVersion(ctx, dbgen.InsertBoundingBoxVersionParams{
			GtfsStaticVersionID: versionID,
			ServerID:            serverID,
			AgencyID:            agencyID,
			MinimumLatitude:     bbox.MinLat,
			MaximumLatitude:     bbox.MaxLat,
			MinimumLongitude:    bbox.MinLon,
			MaximumLongitude:    bbox.MaxLon,
		})
		if err != nil {
			return fmt.Errorf("server %q: insert %s bounding box: %w", server.ServerName, label, err)
		}
		recordedBoxes[boxKey] = boxID
		return nil
	}
	for _, agency := range agencies {
		agencyID := agencyIDs[agency.AgencyID]
		if err := insertBox(models.ServerKey(server.ObaBaseURL, agency.AgencyID), &agencyID, fmt.Sprintf("agency %q", agency.AgencyID)); err != nil {
			return staticVersionRecord{}, err
		}
	}
	if err := insertBox(models.ServerKey(server.ObaBaseURL, ""), nil, "server-wide"); err != nil {
		return staticVersionRecord{}, err
	}

	return staticVersionRecord{versionID: versionID, created: true, boxes: recordedBoxes}, nil
}

// reusableStaticVersion returns the version a publication can reuse instead
// of recording a new one, if any. A version is reusable when it is the newest
// version in the entry's scope and has the same agency set and combined hash.
//
// The scope depends on the entry. Agency-scoped entries on one base URL share
// a database server, so each one's scope is the versions recorded with
// exactly its agency; otherwise the entries would take turns being the
// server's newest version and every restart would record a new one. A
// server-scoped entry's agency set can change between publications, so its
// scope is every version of the server. Matching on the agency set alone
// there would treat a return to an earlier feed (H1 [A], H2 [A B], then H1
// [A]) as unchanged, and history could not show the rollback.
func reusableStaticVersion(ctx context.Context, q *dbgen.Queries, server models.ObaServer, serverID int64, agencyIDs []string, combined string) (int64, bool, error) {
	var (
		versionID int64
		hash      *string
		same      = true
		err       error
	)
	if server.IsServerScoped() {
		var latest dbgen.GetLatestGTFSStaticVersionForServerRow
		latest, err = q.GetLatestGTFSStaticVersionForServer(ctx, dbgen.GetLatestGTFSStaticVersionForServerParams{
			ServerID:  serverID,
			AgencyIds: agencyIDs,
		})
		versionID, hash, same = latest.GtfsStaticVersionID, latest.CombinedContentSha256, latest.SameAgencies
	} else {
		var latest dbgen.GetLatestGTFSStaticVersionForAgenciesRow
		latest, err = q.GetLatestGTFSStaticVersionForAgencies(ctx, dbgen.GetLatestGTFSStaticVersionForAgenciesParams{
			ServerID:  serverID,
			AgencyIds: agencyIDs,
		})
		versionID, hash = latest.GtfsStaticVersionID, latest.CombinedContentSha256
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("server %q: read latest static version: %w", server.ServerName, err)
	}
	return versionID, same && hash != nil && *hash == combined, nil
}

// recordedBoundingBoxes returns the boxes stored with a version for the
// published agencies and the server-wide box, keyed the way
// geo.BoundingBoxStore keys them.
func recordedBoundingBoxes(ctx context.Context, q *dbgen.Queries, server models.ObaServer, agencyIDs []string, versionID int64) (map[string]int64, error) {
	rows, err := q.ListBoundingBoxVersionsForStaticVersion(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("server %q: read bounding boxes of static version %d: %w", server.ServerName, versionID, err)
	}
	published := make(map[string]bool, len(agencyIDs)+1)
	published[""] = true
	for _, agencyID := range agencyIDs {
		published[agencyID] = true
	}
	recorded := make(map[string]int64, len(rows))
	for _, row := range rows {
		agencyID := ""
		if row.ExternalAgencyID != nil {
			agencyID = *row.ExternalAgencyID
		}
		if !published[agencyID] {
			continue
		}
		recorded[models.ServerKey(server.ObaBaseURL, agencyID)] = row.BoundingBoxVersionID
	}
	return recorded, nil
}

// combinedContentSHA256 is the lowercase hex SHA-256 of the feed hashes joined
// with "\n", in configured order.
func combinedContentSHA256(feedHashes []string) string {
	sum := sha256.Sum256([]byte(strings.Join(feedHashes, "\n")))
	return hex.EncodeToString(sum[:])
}

// isSHA256Hex reports whether s is a lowercase hex SHA-256, the form the
// schema's CHECK constraints accept.
func isSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// uniquePublishedAgencies drops empty and repeated agency IDs, keeping the
// first occurrence, since a version lists each agency once.
func uniquePublishedAgencies(agencies []gtfs.StaticPublishedAgency) []gtfs.StaticPublishedAgency {
	seen := make(map[string]bool, len(agencies))
	unique := make([]gtfs.StaticPublishedAgency, 0, len(agencies))
	for _, agency := range agencies {
		if agency.AgencyID == "" || seen[agency.AgencyID] {
			continue
		}
		seen[agency.AgencyID] = true
		unique = append(unique, agency)
	}
	return unique
}
