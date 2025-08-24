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