package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestSecurityHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()

	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing X-Content-Type-Options")
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Errorf("missing Cache-Control")
	}
}

func TestSentryMiddleware(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()

	handler := SentryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200")
	}
}

func TestCachedPromHandler(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_counter",
		Help: "A test counter",
	})
	reg.MustRegister(counter)
	counter.Inc()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := NewCachedPromHandler(ctx, reg, 50*time.Millisecond)

	// wait for cache to populate
	time.Sleep(100 * time.Millisecond)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	if !bytes.Contains(rr.Body.Bytes(), []byte("test_counter 1")) {
		t.Errorf("cache missing test_counter metric")
	}

	// Test early fallback
	handler2 := NewCachedPromHandler(ctx, reg, 1*time.Hour)
	rr2 := httptest.NewRecorder()
	handler2.ServeHTTP(rr2, req)
	if !bytes.Contains(rr2.Body.Bytes(), []byte("test_counter 1")) {
		t.Errorf("fallback missing test_counter metric")
	}
}
