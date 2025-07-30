package gtfs

import (
	"sync"

	remoteGtfs "github.com/jamespfennell/gtfs"
)

// StaticStore is a thread-safe in-memory store for GTFS static bundles,
// indexed by server ID. It allows concurrent access to GTFS data
// using read-write locks using a sync.RWMutex.
type StaticStore struct {
	mu   sync.RWMutex
	data map[int]*remoteGtfs.Static // GTFS Static bundle data of each server, indexed by server ID
}

// NewStaticStore initializes and returns a new instance of StaticStore.
// The underlying map is lazily initialized on first use in Set.
//
// Returns:
//   - *StaticStore: A new, empty StaticStore instance.
func NewStaticStore() *StaticStore {
	return &StaticStore{}
}

// Set stores the given GTFS static data for the specified server ID.
// If the internal map is not initialized, it creates it.
// This method is thread-safe and uses a write lock.
//
// Parameters:
//   - serverID: The unique identifier for the OBA server.
//   - newData: A pointer to the GTFS static data to store.
func (s *StaticStore) Set(serverID int, newData *remoteGtfs.Static) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[int]*remoteGtfs.Static)
	}
	s.data[serverID] = newData
}

// Get retrieves the GTFS static data for the specified server ID.
// This method is thread-safe and uses a read lock.
//
// Parameters:
//   - serverID: The unique identifier for the OBA server.
//
// Returns:
//   - *remoteGtfs.Static: A pointer to the GTFS static data, if present.
//   - bool: True if data exists for the given server ID, false otherwise.
func (s *StaticStore) Get(serverID int) (*remoteGtfs.Static, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.data[serverID]
	return data, exists
}
