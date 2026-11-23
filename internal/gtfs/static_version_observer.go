package gtfs

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

// StaticFeedContent is one configured static feed of a publication and the
// content hash of the artifact that was published from it.
type StaticFeedContent struct {
	// URL is the feed URL as configured, not sanitized. The observer
	// normalizes it.
	URL string
	// ContentSHA256 is the lowercase hex SHA-256 of the downloaded artifact.
	ContentSHA256 string
}

// StaticPublishedAgency is one agency a publication installed a snapshot for.
type StaticPublishedAgency struct {
	AgencyID   string
	AgencyName string
}

// StaticVersionPublication describes one newly installed static snapshot for a
// configured server entry.
type StaticVersionPublication struct {
	Server models.ObaServer
	// PublishedAt is the time passed to StaticStore.ReplaceServerSnapshot.
	PublishedAt time.Time
	// Feeds lists every configured feed URL with its content hash, in
	// configured order.
	Feeds []StaticFeedContent
	// Agencies lists the agencies the snapshot was stored under, in the
	// order they were published.
	Agencies []StaticPublishedAgency
	// BoundingBoxes holds the boxes the publication set, keyed like
	// geo.BoundingBoxStore, captured under the refresh lock so a later
	// publication cannot change them before they are recorded.
	BoundingBoxes map[string]geo.BoundingBox
}

// StaticVersionObserver is called once per server each time a new static
// snapshot is installed with StaticStore.ReplaceServerSnapshot, after the
// bounding boxes for it are updated. It is not called when byte-identical
// feeds only confirm the current snapshot (TouchServerSnapshot).
type StaticVersionObserver func(StaticVersionPublication)

// SetStaticVersionObserver registers the static version publication hook.
func (gs *GtfsService) SetStaticVersionObserver(observer StaticVersionObserver) {
	gs.VersionObserver = observer
}

// reportStaticVersion calls the publication hook. A panicking observer must
// not take down the refresh path, the same as the other observers.
func (gs *GtfsService) reportStaticVersion(publication *StaticVersionPublication) {
	if gs.VersionObserver == nil || publication == nil {
		return
	}
	defer func() { _ = recover() }()
	gs.VersionObserver(*publication)
}

// contentSHA256 returns the lowercase hex SHA-256 of a feed artifact, the
// same form staticArtifactCache records.
func contentSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
