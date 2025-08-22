package config

import (
	"math/rand/v2"
	"sync"
	"time"
)

const (
	BASE_BACKOFF   = 1 * time.Second
	MAX_BACKOFF    = 2 * time.Minute
	BACKOFF_FACTOR = 2.0
	JITTER_FACTOR  = 0.5
)

type backoffData struct {
	BackoffDelay time.Duration
	NextRetryAt  time.Time
}

type BackoffStore struct {
	mu       sync.RWMutex
	backoffs map[int]backoffData
}

func NewBackoffStore() *BackoffStore {
	return &BackoffStore{
		backoffs: make(map[int]backoffData),
	}
}

func (s *BackoffStore) NextRetryAt(serverID int) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if backoff, exists := s.backoffs[serverID]; exists {
		return backoff.NextRetryAt.UTC(), true
	}
	return time.Time{}, false
}

func (s *BackoffStore) UpdateBackoff(serverID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if backoff, exists := s.backoffs[serverID]; exists {
		backoff.BackoffDelay = calculateNewBackoffDelay(backoff.BackoffDelay)
		backoff.NextRetryAt = calculateNextRetryAt(backoff.BackoffDelay)
		s.backoffs[serverID] = backoff
	} else {
		s.backoffs[serverID] = backoffData{
			BackoffDelay: BASE_BACKOFF,
			NextRetryAt:  calculateNextRetryAt(BASE_BACKOFF),
		}
	}
}

func (s *BackoffStore) ResetBackoff(serverID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.backoffs, serverID)
}


func calculateNextRetryAt(backoff time.Duration) time.Time {
	jitter := time.Duration(rand.Float64() * float64(backoff) * JITTER_FACTOR)
	backoff += jitter
	if backoff > MAX_BACKOFF {
		backoff = MAX_BACKOFF
	}
	return time.Now().Add(backoff).UTC()
}

func calculateNewBackoffDelay(backoffDelay time.Duration) time.Duration {
	backoffDelay *= BACKOFF_FACTOR
	if backoffDelay >= MAX_BACKOFF {
		backoffDelay = MAX_BACKOFF
	}
	return backoffDelay
}
