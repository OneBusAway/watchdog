package config

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDoWithBackoff(t *testing.T) {
	tests := []struct {
		name          string
		maxRetries    int
		ctxTimeout    time.Duration
		handler       func(req *http.Request) (*http.Response, error)
		expectErr     string
		expectCalls   int
		expectSuccess bool
	}{
		{
			name:       "success on first try",
			maxRetries: 3,
			handler: func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
			},
			expectErr:     "",
			expectCalls:   1,
			expectSuccess: true,
		},
		{
			name:       "max retries exceeded",
			maxRetries: 2,
			handler: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("mock error")
			},
			expectErr:   "max retries exceeded",
			expectCalls: 3,
		},
		{
			name:       "context cancelled before success",
			maxRetries: 0,
			ctxTimeout: 50 * time.Millisecond,
			handler: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("fail")
			},
			expectErr:   "context deadline exceeded",
			expectCalls: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockRoundTripper{handler: tt.handler}
			client := &http.Client{Transport: mock}
			req, _ := http.NewRequest("GET", "http://example.com", nil)

			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}

			resp, err := DoWithBackoff(ctx, client, req, tt.maxRetries)

			if tt.expectErr == "" && err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if tt.expectErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing %q, got %v", tt.expectErr, err)
				}
			}
			if tt.expectSuccess && resp == nil {
				t.Fatalf("expected response, got nil")
			}

			if tt.expectCalls >= 0 && mock.calls != tt.expectCalls {
				t.Errorf("expected %d calls, got %d", tt.expectCalls, mock.calls)
			}
		})
	}
}

func TestCalculateNewBackoffDelay(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{
			name:     "normal case",
			input:    1 * time.Second,
			expected: 1 * time.Second * BACKOFF_FACTOR,
		},
		{
			name:     "capped at max backoff",
			input:    MAX_BACKOFF,
			expected: MAX_BACKOFF,
		},
		{
			name:     "above max backoff",
			input:    MAX_BACKOFF * 2,
			expected: MAX_BACKOFF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateNewBackoffDelay(tt.input)
			if got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestCalculateNextRetryAt(t *testing.T) {
	backoff := 1 * time.Second
	now := time.Now().UTC()

	got := calculateNextRetryAt(backoff)

	if got.Before(now.Add(backoff)) {
		t.Errorf("expected >= %v, got %v", now.Add(backoff), got)
	}

	maxWithJitter := backoff + time.Duration(float64(backoff)*JITTER_FACTOR)
	if maxWithJitter > MAX_BACKOFF {
		maxWithJitter = MAX_BACKOFF
	}
	if got.After(now.Add(maxWithJitter)) {
		t.Errorf("expected <= %v, got %v", now.Add(maxWithJitter), got)
	}
}

func TestBackoffStore(t *testing.T) {
	store := NewBackoffStore()
	serverID := 42

	t.Run("NextRetryAt returns false when no entry", func(t *testing.T) {
		_, ok := store.NextRetryAt(serverID)
		if ok {
			t.Errorf("expected no backoff entry, got ok=true")
		}
	})

	t.Run("UpdateBackoff creates new entry", func(t *testing.T) {
		store.UpdateBackoff(serverID)
		got, ok := store.NextRetryAt(serverID)
		if !ok {
			t.Fatalf("expected entry to exist after UpdateBackoff")
		}

		now := time.Now().UTC()
		if got.Before(now.Add(BASE_BACKOFF)) {
			t.Errorf("expected next retry >= %v, got %v", now.Add(BASE_BACKOFF), got)
		}
	})

	t.Run("UpdateBackoff increases delay", func(t *testing.T) {
		before, _ := store.NextRetryAt(serverID)
		store.UpdateBackoff(serverID)
		after, _ := store.NextRetryAt(serverID)
		
		if !after.After(before) {
			t.Errorf("expected NextRetryAt to move forward after backoff increase, got before=%v after=%v", before, after)
		}
	})

	t.Run("ResetBackoff deletes entry", func(t *testing.T) {
		store.ResetBackoff(serverID)
		_, ok := store.NextRetryAt(serverID)
		if ok {
			t.Errorf("expected no entry after ResetBackoff")
		}
	})
}
