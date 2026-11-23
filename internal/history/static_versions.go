package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/geo"
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
	// versions maps a server key (models.ObaServer.ServerKey) to the ID of
	// its current gtfs_static_version row.
	versions map[string]int64
	// boxes maps a bounding box key (the geo.BoundingBoxStore key) to the
	// ID of its current bounding_box_version row.
	boxes map[string]int64
	// boxKeys maps a server key to the box keys its current version set, so
	// the next version replaces exactly those.
	boxKeys map[string][]string
}

func newCurrentStatic() *currentStatic {
	return &currentStatic{
		versions: make(map[string]int64),
		boxes:    make(map[string]int64),
		boxKeys:  make(map[string][]string),
	}
}

func (c *currentStatic) version(serverKey string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.versions[serverKey]
	return id, ok
}

func (c *currentStatic) box(boxKey string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.boxes[boxKey]
	return id, ok
}

// set makes versionID and boxes current for serverKey, dropping the boxes
// its previous version set.
func (c *currentStatic) set(serverKey string, versionID int64, boxes map[string]int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(serverKey)
	c.versions[serverKey] = versionID
	keys := make([]string, 0, len(boxes))
	for key, id := range boxes {
		c.boxes[key] = id
		keys = append(keys, key)
	}
	c.boxKeys[serverKey] = keys
}

// forget drops serverKey's current version and boxes.
func (c *currentStatic) forget(serverKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(serverKey)
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
	return h.static.version(serverKey)
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

// onStaticVersion is called after a new static version is recorded and
// committed. It does nothing yet; the stop metadata (B3) and unmatched trip
// (#157) recording hook in here.
func (h *HistoryService) onStaticVersion(_ context.Context, _ models.ObaServer, _ int64) {}

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
	var recorded staticVersionRecord
	committed := h.write(ctx, staticVersionStep, publicationTags(publication.Server), func(q *dbgen.Queries) error {
		var err error
		recorded, err = recordStaticVersion(ctx, q, publication, h.boxes, h.resolver, h.version)
		return err
	})
	if !committed {
		// The snapshot in use is no longer the recorded current version.
		// Pointing new history at the old one would be wrong, so point at
		// none until a publication records successfully.
		h.static.forget(serverKey)
		return
	}
	h.static.set(serverKey, recorded.versionID, recorded.boxes)
	if recorded.created {
		h.onStaticVersion(ctx, publication.Server, recorded.versionID)
	}
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
	// created is false when the server's newest version already had the
	// same combined hash and was reused.
	created bool
	// boxes maps each bounding box key to the ID recorded for it.
	boxes map[string]int64
}

// recordStaticVersion writes one publication with q:
//
//  1. Ensure the server and each static source.
//  2. Compute the combined content hash.
//  3. If the server's newest version has the same combined hash, reuse it
//     (a restart with unchanged feeds).
//  4. Otherwise insert the version, one source row per feed and one agency
//     row per published agency.
//  5. Insert the bounding boxes boxes holds for each published agency and
//     the server-wide box, skipping keys without a box.
func recordStaticVersion(ctx context.Context, q *dbgen.Queries, publication gtfs.StaticVersionPublication, boxes *geo.BoundingBoxStore, resolver *ServerNameResolver, parserVersion string) (staticVersionRecord, error) {
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

	latest, err := q.GetLatestGTFSStaticVersionForServer(ctx, serverID)
	switch {
	case err == nil && latest.CombinedContentSha256 != nil && *latest.CombinedContentSha256 == combined:
		recordedBoxes, err := recordedBoundingBoxes(ctx, q, server, latest.GtfsStaticVersionID)
		if err != nil {
			return staticVersionRecord{}, err
		}
		return staticVersionRecord{versionID: latest.GtfsStaticVersionID, boxes: recordedBoxes}, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return staticVersionRecord{}, fmt.Errorf("server %q: read latest static version: %w", server.ServerName, err)
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

	agencies := uniquePublishedAgencies(publication.Agencies)
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
	if boxes != nil {
		insertBox := func(boxKey string, agencyID *int64, label string) error {
			bbox, ok := boxes.Get(boxKey)
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
	}

	return staticVersionRecord{versionID: versionID, created: true, boxes: recordedBoxes}, nil
}

// recordedBoundingBoxes returns the boxes stored with a version, keyed the
// way geo.BoundingBoxStore keys them.
func recordedBoundingBoxes(ctx context.Context, q *dbgen.Queries, server models.ObaServer, versionID int64) (map[string]int64, error) {
	rows, err := q.ListBoundingBoxVersionsForStaticVersion(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("server %q: read bounding boxes of static version %d: %w", server.ServerName, versionID, err)
	}
	recorded := make(map[string]int64, len(rows))
	for _, row := range rows {
		agencyID := ""
		if row.ExternalAgencyID != nil {
			agencyID = *row.ExternalAgencyID
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
