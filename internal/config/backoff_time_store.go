package config

import (
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/utils"
)

const (
	// BASE_BACKOFF is the initial backoff delay before the first retry.
	BASE_BACKOFF = utils.BaseBackoff
	// MAX_BACKOFF is the upper limit for the backoff delay.
	MAX_BACKOFF = utils.MaxBackoff
)

// backoffData holds the backoff delay and the timestamp of the next retry attempt
// for a given server.
type backoffData struct {
	// BackoffDelay is the current delay duration before retrying.
	BackoffDelay time.Duration
	// NextRetryAt is the absolute timestamp when the next retry can be attempted.
	NextRetryAt time.Time
}

// BackoffStore manages backoff state for multiple servers.
// It is safe for concurrent use across goroutines.
type BackoffStore struct {
	mu       sync.RWMutex
	backoffs map[string]backoffData
}

// NewBackoffStore creates and returns a new BackoffStore instance.
func NewBackoffStore() *BackoffStore {
	return &BackoffStore{
		backoffs: make(map[string]backoffData),
	}
}

// NextRetryAt retrieves the next retry time for the given server key.
// It returns the timestamp in UTC and a boolean indicating whether the server has an active backoff.
func (s *BackoffStore) NextRetryAt(serverKey string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if backoff, exists := s.backoffs[serverKey]; exists {
		return backoff.NextRetryAt.UTC(), true
	}
	return time.Time{}, false
}

// UpdateBackoff updates the backoff delay and next retry time for the given server key.
// If no backoff exists for the server, it initializes one with BASE_BACKOFF.
func (s *BackoffStore) UpdateBackoff(serverKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if backoff, exists := s.backoffs[serverKey]; exists {
		backoff.BackoffDelay = utils.CalculateNewBackoffDelay(backoff.BackoffDelay)
		backoff.NextRetryAt = utils.CalculateNextRetryAt(backoff.BackoffDelay)
		s.backoffs[serverKey] = backoff
	} else {
		s.backoffs[serverKey] = backoffData{
			BackoffDelay: BASE_BACKOFF,
			NextRetryAt:  utils.CalculateNextRetryAt(BASE_BACKOFF),
		}
	}
}

// ResetBackoff removes any existing backoff data for the given server key.
func (s *BackoffStore) ResetBackoff(serverKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.backoffs, serverKey)
}
