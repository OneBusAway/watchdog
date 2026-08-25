package utils

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
			name:       "success after a few retries",
			maxRetries: 3,
			handler: func() func(req *http.Request) (*http.Response, error) {
				calls := 0
				return func(req *http.Request) (*http.Response, error) {
					calls++
					if calls < 3 {
						return nil, errors.New("transient")
					}
					return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
				}
			}(),
			expectErr:     "",
			expectCalls:   3,
			expectSuccess: true,
		},
		{
			name:       "exhausts retries",
			maxRetries: 2,
			handler: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("permanent")
			},
			expectErr:     "max retries exceeded",
			expectCalls:   3,
			expectSuccess: false,
		},
		{
			name:       "context cancelled returns promptly",
			maxRetries: 5,
			ctxTimeout: 50 * time.Millisecond,
			handler: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("keep failing")
			},
			expectErr:     "context deadline exceeded",
			expectSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}

			var calls int
			client := &http.Client{
				Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					return tt.handler(req)
				}),
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}

			resp, err := DoWithBackoff(ctx, client, req, tt.maxRetries)
			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing %q, got %q", tt.expectErr, err.Error())
				}
				if resp != nil {
					resp.Body.Close()
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			} else if resp == nil {
				t.Fatalf("expected non-nil response on success")
			} else if resp.StatusCode != 200 {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}
			if tt.expectSuccess {
				if resp == nil || resp.StatusCode != 200 {
					t.Fatalf("expected successful response, got %v", resp)
				}
				resp.Body.Close()
			}
			if tt.expectCalls > 0 && calls != tt.expectCalls {
				t.Fatalf("expected %d calls, got %d", tt.expectCalls, calls)
			}
		})
	}
}

// roundTripperFunc adapts a function to http.RoundTripper so we can count
// invocations without spinning up a real server.
type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
