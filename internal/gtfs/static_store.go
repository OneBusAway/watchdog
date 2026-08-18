package gtfs

import (
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// StaticStore is a thread-safe in-memory store for GTFS static bundles,
// indexed by agency ID. It allows concurrent access to GTFS data
// using read-write locks using a sync.RWMutex.
type StaticStore struct {
	mu   sync.RWMutex
	data map[string]*models.StaticData

	// lastFetched records when Watchdog last downloaded the GTFS static bundle
	// for each agency. It backs the `gtfs_bundle_last_fetched_timestamp_seconds`
	// Prometheus metric.
	//
	// Why this exists: the OBA server's unmatched-stop list is relative to the
	// bundle it has active, while Watchdog resolves those IDs against its own
	// bundle snapshot (refreshed every 24h). The two can drift apart, causing
	// `oba_unmatched_stop_unresolved` to be non-zero even though there is no real
	// problem with the feed.
	//
	// How it helps:
	//   - Shows how stale Watchdog's snapshot is relative to the OBA server.
	//   - Correlates `oba_unmatched_stop_unresolved` with bundle age: drift right
	//     after a fresh fetch indicates a genuine feed content mismatch, whereas
	//     drift on an old snapshot is an expected refresh-timing artifact.
	lastFetched map[string]time.Time
}

// NewStaticStore initializes and returns a new instance of StaticStore.
// The underlying map is lazily initialized on first use in Set.
//
// Returns:
//   - *StaticStore: A new, empty StaticStore instance.
func NewStaticStore() *StaticStore {
	return &StaticStore{}
}

// Set stores the given GTFS static data for the specified agency ID.
// If the internal map is not initialized, it creates it.
// This method is thread-safe and uses a write lock.
//
// Parameters:
//   - agencyID: The unique identifier for the agency.
//   - newData: A pointer to the GTFS static data to store.
func (s *StaticStore) Set(agencyID string, newData *models.StaticData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]*models.StaticData)
	}
	s.data[agencyID] = newData
}

// Get retrieves the GTFS static data for the specified agency ID.
// This method is thread-safe and uses a read lock.
//
// Parameters:
//   - agencyID: The unique identifier for the agency.
//
// Returns:
//   - *remoteGtfs.Static: A pointer to the GTFS static data, if present.
//   - bool: True if data exists for the given agency ID, false otherwise.
func (s *StaticStore) Get(agencyID string) (*models.StaticData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.data[agencyID]
	return data, exists
}

// SetFetchTime records when the GTFS static bundle was last downloaded for the
// specified agency ID. If the internal map is not initialized, it creates it.
// This method is thread-safe and uses a write lock.
func (s *StaticStore) SetFetchTime(agencyID string, fetchTime time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastFetched == nil {
		s.lastFetched = make(map[string]time.Time)
	}
	s.lastFetched[agencyID] = fetchTime
}

// GetFetchTime returns when the GTFS static bundle was last downloaded for the
// specified agency ID. It returns the timestamp and a boolean indicating whether
// a fetch time is recorded.
func (s *StaticStore) GetFetchTime(agencyID string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fetchTime, exists := s.lastFetched[agencyID]
	return fetchTime, exists
}
