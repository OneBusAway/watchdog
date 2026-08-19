package config

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

const (
	// BASE_BACKOFF is the initial backoff delay before the first retry.
	BASE_BACKOFF = 1 * time.Second
	// MAX_BACKOFF is the upper limit for the backoff delay.
	MAX_BACKOFF = 2 * time.Minute
	// BACKOFF_FACTOR is the multiplier used to increase the backoff delay after each retry.
	BACKOFF_FACTOR = 2.0
	// JITTER_FACTOR is the proportion of randomness applied to the backoff delay.
	// It helps avoid synchronized retries across multiple clients.
	JITTER_FACTOR = 0.5
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
		backoff.BackoffDelay = calculateNewBackoffDelay(backoff.BackoffDelay)
		backoff.NextRetryAt = calculateNextRetryAt(backoff.BackoffDelay)
		s.backoffs[serverKey] = backoff
	} else {
		s.backoffs[serverKey] = backoffData{
			BackoffDelay: BASE_BACKOFF,
			NextRetryAt:  calculateNextRetryAt(BASE_BACKOFF),
		}
	}
}

// ResetBackoff removes any existing backoff data for the given server key.
func (s *BackoffStore) ResetBackoff(serverKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.backoffs, serverKey)
}

// DoWithBackoff executes an HTTP request with exponential backoff on failure.
// - If maxRetries is zero, it retries indefinitely.
// - If the context is canceled, it returns immediately.
// It applies jitter to avoid synchronized retries across clients.
func DoWithBackoff(ctx context.Context, client *http.Client, req *http.Request, maxRetries int) (*http.Response, error) {
	backoffDelay := BASE_BACKOFF
	retries := 0

	for {
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}

		// If maxRetries is greater than zero and reached, stop retrying.
		if maxRetries > 0 && retries >= maxRetries {
			return nil, fmt.Errorf("max retries exceeded: %w", err)
		}

		// Wait for either the backoff delay or context cancellation.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoffDelay):
		}

		backoffDelay = calculateNewBackoffDelay(backoffDelay)
		retries++
	}
}

// calculateNextRetryAt returns the next retry time by adding jitter to the given backoff duration.
// The result is capped at MAX_BACKOFF and returned as a UTC timestamp.
func calculateNextRetryAt(backoff time.Duration) time.Time {
	// Adding jitter for backoff retries. Cryptographic randomness is not required.
	// #nosec G404
	jitter := time.Duration(rand.Float64() * float64(backoff) * JITTER_FACTOR)
	backoff += jitter
	if backoff > MAX_BACKOFF {
		backoff = MAX_BACKOFF
	}
	return time.Now().Add(backoff).UTC()
}

// calculateNewBackoffDelay increases the given backoff delay by BACKOFF_FACTOR.
// The result is capped at MAX_BACKOFF.
func calculateNewBackoffDelay(backoffDelay time.Duration) time.Duration {
	backoffDelay *= BACKOFF_FACTOR
	if backoffDelay >= MAX_BACKOFF {
		backoffDelay = MAX_BACKOFF
	}
	return backoffDelay
}
