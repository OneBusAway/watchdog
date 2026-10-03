package gtfs

import (
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// FeedObservation describes one configured feed fetch. PayloadHash is
// canonical and excludes the FeedHeader; SourceTimestamp is retained
// separately.
type FeedObservation struct {
	FeedID                  string
	FeedURL                 string
	Success                 bool
	ObservedAt              time.Time
	SourceTimestamp         *time.Time
	PayloadHash             string
	VehicleEntities         int
	VehicleTimestampMissing int
}

func (s *RealtimeStore) SetObserver(observer func(models.ObaServer, FeedObservation)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}

func (s *RealtimeStore) SetConfigurationObserver(observer func(models.ObaServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configurationObserver = observer
}

func (s *RealtimeStore) ReconcileConfiguration(server models.ObaServer) {
	s.mu.RLock()
	observer := s.configurationObserver
	s.mu.RUnlock()
	if observer != nil {
		observer(server)
	}
}

func (s *RealtimeStore) Observe(server models.ObaServer, observation FeedObservation) {
	s.mu.RLock()
	observer := s.observer
	s.mu.RUnlock()
	if observer != nil {
		observer(server, observation)
	}
}

// RealtimeStore is used to store GTFS-RT data
// fetched once by a designated function. This avoids making multiple API calls for the same data
// and allows other components to reuse the parsed result safely across goroutines.
//
// It provides a thread-safe way to store and retrieve parsed GTFS-RT data.
// It ensures that multiple goroutines can safely read the same data after it is set once.
type RealtimeStore struct {
	mu                    sync.RWMutex
	data                  map[string]*models.RealtimeData
	observer              func(models.ObaServer, FeedObservation)
	configurationObserver func(models.ObaServer)
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
//   - serverKey: The composite server key (oba_base_url + agency_id).
//   - newData: The parsed GTFS-RT feed to store.
func (s *RealtimeStore) Set(serverKey string, newData *models.RealtimeData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]*models.RealtimeData)
	}
	s.data[serverKey] = newData
}

// Get retrieves the most recently stored GTFS-RT data in a thread-safe way.
// It can be safely called by multiple consumers concurrently.
//
// Parameters:
//   - serverKey: The composite server key (oba_base_url + agency_id).
//
// Returns:
//   - A pointer to the parsed GTFS-RT feed, or nil if not set.
func (s *RealtimeStore) Get(serverKey string) *models.RealtimeData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[serverKey]
}

// Prune removes every stored feed whose server key the keep predicate
// rejects and returns the removed keys. See StaticStore.Prune for why this
// exists.
func (s *RealtimeStore) Prune(keep func(serverKey string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []string
	for serverKey := range s.data {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.data, serverKey)
		}
	}
	return removed
}
