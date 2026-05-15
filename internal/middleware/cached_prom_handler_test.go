package middleware

// cached_prom_handler_test.go contains unit tests for the CachedPromHandler
// defined in cached_prom_handler.go.
//
// Author: Zala Vishmayraj
//
// Run tests:
//   go test ./internal/middleware/... -run TestCachedPromHandler
//   go test ./internal/middleware/... -run TestCachedPromHandler -v
//   go test ./internal/middleware/... -race (concurrent safety)

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCachedPromHandler(t *testing.T) {
	t.Run("Empty cache falls back to live handler", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry := prometheus.NewRegistry()
		handler := NewCachedPromHandler(ctx, registry, 10*time.Second)

		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}
	})

	t.Run("Populated cache serves cached content with correct Content-Type", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry := prometheus.NewRegistry()
		handler := NewCachedPromHandler(ctx, registry, 50*time.Millisecond)

		// Wait for the cache to be populated by the refresh loop
		time.Sleep(200 * time.Millisecond)

		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}

		contentType := rec.Header().Get("Content-Type")
		if !strings.Contains(contentType, "text/plain") {
			t.Errorf("Expected Content-Type to contain text/plain, got %q", contentType)
		}
	})

	t.Run("Concurrent reads do not race", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		registry := prometheus.NewRegistry()
		handler := NewCachedPromHandler(ctx, registry, 50*time.Millisecond)

		time.Sleep(200 * time.Millisecond)

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/metrics", nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
			}()
		}
		wg.Wait()
	})
}