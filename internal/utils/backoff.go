package utils

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"
)

const (
	// BaseBackoff is the initial backoff delay before the first retry.
	BaseBackoff = 1 * time.Second
	// MaxBackoff is the upper limit for the backoff delay.
	MaxBackoff = 2 * time.Minute
	// BackoffFactor is the multiplier used to increase the backoff delay after each retry.
	BackoffFactor = 2.0
	// JitterFactor is the proportion of randomness applied to the backoff delay.
	// It helps avoid synchronized retries across multiple clients.
	JitterFactor = 0.5
)

// DoWithBackoff executes an HTTP request with exponential backoff on failure.
// - If maxRetries is zero, it retries indefinitely.
// - If the context is canceled, it returns immediately.
// It applies jitter to avoid synchronized retries across clients.
//
// This function lives in the utils package (rather than config) so packages
// that import config (or are imported by config) can use it without creating
// an import cycle. config.BackoffStore still owns backoff *state* for the
// per-server metrics collector; this function is the stateless executor.
func DoWithBackoff(ctx context.Context, client *http.Client, req *http.Request, maxRetries int) (*http.Response, error) {
	backoffDelay := BaseBackoff
	retries := 0

	for {
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}

		if maxRetries > 0 && retries >= maxRetries {
			return nil, fmt.Errorf("max retries exceeded: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoffDelay):
		}

		backoffDelay = calculateNewBackoffDelay(backoffDelay)
		retries++
	}
}

// CalculateNextRetryAt returns the next retry time by adding jitter to the
// given backoff duration. The result is capped at MaxBackoff and returned as a
// UTC timestamp.
func CalculateNextRetryAt(backoff time.Duration) time.Time {
	jitter := time.Duration(rand.Float64() * float64(backoff) * JitterFactor)
	backoff += jitter
	if backoff > MaxBackoff {
		backoff = MaxBackoff
	}
	return time.Now().Add(backoff).UTC()
}

// CalculateNewBackoffDelay increases the given backoff delay by BackoffFactor.
// The result is capped at MaxBackoff.
func CalculateNewBackoffDelay(backoffDelay time.Duration) time.Duration {
	backoffDelay *= BackoffFactor
	if backoffDelay >= MaxBackoff {
		backoffDelay = MaxBackoff
	}
	return backoffDelay
}

func calculateNextRetryAt(backoff time.Duration) time.Time {
	jitter := time.Duration(rand.Float64() * float64(backoff) * JitterFactor)
	backoff += jitter
	if backoff > MaxBackoff {
		backoff = MaxBackoff
	}
	return time.Now().Add(backoff).UTC()
}

func calculateNewBackoffDelay(backoffDelay time.Duration) time.Duration {
	backoffDelay *= BackoffFactor
	if backoffDelay >= MaxBackoff {
		backoffDelay = MaxBackoff
	}
	return backoffDelay
}
