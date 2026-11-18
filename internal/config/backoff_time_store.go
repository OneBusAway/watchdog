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
	// base is the first delay and max caps every delay.
	base, max time.Duration
}

// NewBackoffStore creates a BackoffStore with the API-ping limits,
// BASE_BACKOFF and MAX_BACKOFF.
func NewBackoffStore() *BackoffStore {
	return NewBackoffStoreWithLimits(BASE_BACKOFF, MAX_BACKOFF)
}

// NewBackoffStoreWithLimits creates a BackoffStore whose delays start at base
// and never exceed max.
func NewBackoffStoreWithLimits(base, max time.Duration) *BackoffStore {
	if base <= 0 {
		base = BASE_BACKOFF
	}
	if max < base {
		max = base
	}
	return &BackoffStore{
		backoffs: make(map[string]backoffData),
		base:     base,
		max:      max,
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
// If no backoff exists for the server, it initializes one with the store's base delay.
func (s *BackoffStore) UpdateBackoff(serverKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delay := s.base
	if backoff, exists := s.backoffs[serverKey]; exists && backoff.BackoffDelay >= s.base {
		delay = utils.NextBackoffDelayWithCap(backoff.BackoffDelay, s.max)
	}
	s.backoffs[serverKey] = backoffData{
		BackoffDelay: delay,
		NextRetryAt:  utils.NextRetryAtWithCap(delay, s.max),
	}
}

// DeferUntil sets a fixed next retry time for the given key, for a failure
// that will not clear up on its own soon. A later UpdateBackoff starts again
// from the base delay.
func (s *BackoffStore) DeferUntil(serverKey string, retryAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.backoffs[serverKey] = backoffData{NextRetryAt: retryAt.UTC()}
}

// ResetBackoff removes any existing backoff data for the given server key.
func (s *BackoffStore) ResetBackoff(serverKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.backoffs, serverKey)
}

// Prune removes the backoff state of every server key the keep predicate
// rejects and returns the removed keys. See gtfs.StaticStore.Prune for why
// this exists.
//
// Backoff state has no TTL of its own — ResetBackoff is only reached by a
// successful ping — so a departed server's entry would otherwise outlive the
// configuration that created it. Beyond the leak, dropping it means a server
// that is later re-added starts from a clean slate instead of inheriting a
// stale NextRetryAt that skips its first collection ticks.
func (s *BackoffStore) Prune(keep func(serverKey string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []string
	for serverKey := range s.backoffs {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.backoffs, serverKey)
		}
	}
	return removed
}
