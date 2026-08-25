package gtfs

import (
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// StaticStore is a thread-safe in-memory store for GTFS static bundles,
// indexed by server key (oba_base_url + agency_id). It allows concurrent access
// to GTFS data using read-write locks using a sync.RWMutex.
type StaticStore struct {
	mu   sync.RWMutex
	data map[string]*models.StaticData

	// lastFetched records when Watchdog last downloaded the GTFS static bundle
	// for each server key. It backs the `gtfs_bundle_last_fetched_timestamp_seconds`
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

// Set stores the given GTFS static data for the specified server key.
// If the internal map is not initialized, it creates it.
// This method is thread-safe and uses a write lock.
//
// Parameters:
//   - serverKey: The composite server key (oba_base_url + agency_id).
//   - newData: A pointer to the GTFS static data to store.
func (s *StaticStore) Set(serverKey string, newData *models.StaticData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]*models.StaticData)
	}
	s.data[serverKey] = newData
}

// Get retrieves the GTFS static data for the specified server key.
// This method is thread-safe and uses a read lock.
//
// Parameters:
//   - serverKey: The composite server key (oba_base_url + agency_id).
//
// Returns:
//   - *remoteGtfs.Static: A pointer to the GTFS static data, if present.
//   - bool: True if data exists for the given server key, false otherwise.
func (s *StaticStore) Get(serverKey string) (*models.StaticData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.data[serverKey]
	return data, exists
}

// Range invokes fn for every (serverKey, *StaticData) pair in the store.
// Iteration stops early if fn returns false. Used by the server-scope
// resolver to enumerate agencies whose static bundles are stored for a
// particular oba_base_url.
//
// The store is read-locked for the duration of iteration; callers must not
// call Set / SetFetchTime from inside fn or they will deadlock.
func (s *StaticStore) Range(fn func(serverKey string, data *models.StaticData) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.data {
		if !fn(k, v) {
			return
		}
	}
}

// SetFetchTime records when the GTFS static bundle was last downloaded for the
// specified server key. If the internal map is not initialized, it creates it.
// This method is thread-safe and uses a write lock.
func (s *StaticStore) SetFetchTime(serverKey string, fetchTime time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastFetched == nil {
		s.lastFetched = make(map[string]time.Time)
	}
	s.lastFetched[serverKey] = fetchTime
}

// GetFetchTime returns when the GTFS static bundle was last downloaded for the
// specified server key. It returns the timestamp and a boolean indicating whether
// a fetch time is recorded.
func (s *StaticStore) GetFetchTime(serverKey string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fetchTime, exists := s.lastFetched[serverKey]
	return fetchTime, exists
}
