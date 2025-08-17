package gtfs

import (
	"sync"

	"watchdog.onebusaway.org/internal/models"
)

// RealtimeStore is used to store GTFS-RT data
// fetched once by a designated function. This avoids making multiple API calls for the same data
// and allows other components to reuse the parsed result safely across goroutines.
//
// It provides a thread-safe way to store and retrieve parsed GTFS-RT data.
// It ensures that multiple goroutines can safely read the same data after it is set once.
type RealtimeStore struct {
	mu   sync.RWMutex
	data *models.RealtimeData
}

// NewRealtimeStore creates and returns a new empty RealtimeStore instance.
//
// Usage:
//
//	store := gtfs.NewRealtimeStore()
func NewRealtimeStore() *RealtimeStore {
	return &RealtimeStore{}
}

// Set stores the latest parsed GTFS-RT data in a thread-safe way.
// It is typically called once by the function responsible for fetching the feed.
//
// Parameters:
//   - newData: The parsed GTFS-RT feed to store.
func (s *RealtimeStore) Set(newData *models.RealtimeData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = newData
}

// Get retrieves the most recently stored GTFS-RT data in a thread-safe way.
// It can be safely called by multiple consumers concurrently.
//
// Returns:
//   - A pointer to the parsed GTFS-RT feed, or nil if not set.
func (s *RealtimeStore) Get() *models.RealtimeData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}
