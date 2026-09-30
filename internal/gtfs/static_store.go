package gtfs

import (
	"reflect"
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

type StaticRefreshState struct {
	LastAttempt time.Time
	Success     bool
	Retrying    bool
	GaveUp      bool
}

// StaticStore is a thread-safe in-memory store for GTFS static bundles,
// indexed by server key (oba_base_url + agency_id). It allows concurrent access
// to GTFS data using read-write locks using a sync.RWMutex.
type StaticStore struct {
	mu               sync.RWMutex
	refreshMu        sync.Mutex
	data             map[string]*models.StaticData
	schedules        *ScheduleStore
	configured       map[string]models.ObaServer
	configurationSet bool

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
	lastFetched  map[string]time.Time
	refreshState map[string]StaticRefreshState
}

// NewStaticStore initializes and returns a new instance of StaticStore.
// The underlying map is lazily initialized on first use in Set.
//
// Returns:
//   - *StaticStore: A new, empty StaticStore instance.
func NewStaticStore() *StaticStore {
	return &StaticStore{schedules: NewScheduleStore()}
}

func (s *StaticStore) ScheduleStore() *ScheduleStore {
	s.mu.RLock()
	schedules := s.schedules
	s.mu.RUnlock()
	if schedules != nil {
		return schedules
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedules == nil {
		s.schedules = NewScheduleStore()
	}
	return s.schedules
}

// WithRefreshLock serializes static publication with configuration pruning.
func (s *StaticStore) WithRefreshLock(fn func()) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	fn()
}

func (s *StaticStore) SetConfiguredServers(servers []models.ObaServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured = make(map[string]models.ObaServer, len(servers))
	for _, server := range servers {
		s.configured[server.ServerKey()] = server
	}
	s.configurationSet = true
}

func (s *StaticStore) IsConfigured(server models.ObaServer) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.configurationSet {
		return true
	}
	configured, ok := s.configured[server.ServerKey()]
	return ok && reflect.DeepEqual(configured, server)
}

// ReplaceServerSnapshot publishes one complete static snapshot and retires
// server-scoped agencies that disappeared from it.
func (s *StaticStore) ReplaceServerSnapshot(server models.ObaServer, snapshots map[string]*models.StaticData, fetchedAt time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]*models.StaticData)
	}
	if s.lastFetched == nil {
		s.lastFetched = make(map[string]time.Time)
	}
	var removed []string
	if server.IsServerScoped() {
		for key := range s.data {
			if server.OwnsServerKey(key) {
				if _, retained := snapshots[key]; !retained {
					removed = append(removed, key)
					delete(s.data, key)
					delete(s.lastFetched, key)
				}
			}
		}
	}
	for key, snapshot := range snapshots {
		s.data[key] = snapshot
		s.lastFetched[key] = fetchedAt
	}
	return removed
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

func (s *StaticStore) SetRefreshState(server models.ObaServer, state StaticRefreshState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshState == nil {
		s.refreshState = make(map[string]StaticRefreshState)
	}
	s.refreshState[server.ServerKey()] = state
}

func (s *StaticStore) GetRefreshState(server models.ObaServer) (StaticRefreshState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.refreshState[server.ServerKey()]
	if !ok && server.AgencyID != "" {
		state, ok = s.refreshState[models.ServerKey(server.ObaBaseURL, "")]
	}
	return state, ok
}

func (s *StaticStore) BundleState(server models.ObaServer, now time.Time, currentMaxAge time.Duration) (current, usable bool) {
	usable = s.ScheduleStore().CoversDate(server.ServerKey(), now)
	fetchedAt, fetched := s.GetFetchTime(server.ServerKey())
	refresh, attempted := s.GetRefreshState(server)
	current = usable && fetched && attempted && refresh.Success && !fetchedAt.After(now) && now.Sub(fetchedAt) <= currentMaxAge
	return current, usable
}

// Prune removes every entry whose server key the keep predicate rejects,
// including its recorded fetch time, and returns the removed keys.
//
// Watchdog's server list can change at runtime (--config-url), and without
// this a departed server's parsed bundle stays resident for the life of the
// process. Callers pass a predicate rather than a key set because a
// server-scoped entry legitimately owns every key under its oba_base_url.
func (s *StaticStore) Prune(keep func(serverKey string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []string
	for serverKey := range s.data {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.data, serverKey)
			delete(s.lastFetched, serverKey)
		}
	}
	// A fetch time can outlive its bundle if a download failed after the
	// timestamp was recorded, so sweep that map independently.
	for serverKey := range s.lastFetched {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.lastFetched, serverKey)
		}
	}
	for serverKey := range s.refreshState {
		if !keep(serverKey) {
			delete(s.refreshState, serverKey)
		}
	}
	if s.schedules != nil {
		removed = append(removed, s.schedules.Prune(keep)...)
	}
	return removed
}
