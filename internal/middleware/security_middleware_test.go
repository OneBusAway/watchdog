package middleware

// security_middleware_test.go contains unit tests for the SecurityHeaders middleware
// defined in security_middleware.go.
//
// Author: Zala Vishmayraj
//
// Run tests:
//   go test ./internal/middleware/... -run TestSecurityHeaders
//   go test ./internal/middleware/... -run TestSecurityHeaders -v

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	t.Run("All security headers are set with correct values", func(t *testing.T) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		handler := SecurityHeaders(next)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		expectedHeaders := map[string]string{
			"X-Content-Type-Options":        "nosniff",
			"Cache-Control":                 "no-store, no-cache, must-revalidate",
			"Pragma":                        "no-cache",
			"Cross-Origin-Opener-Policy":    "same-origin",
			"Cross-Origin-Resource-Policy":  "same-origin",
			"X-XSS-Protection":              "1; mode=block",
			"Content-Security-Policy":       "default-src 'self'",
		}

		for header, expected := range expectedHeaders {
			got := rec.Header().Get(header)
			if got != expected {
				t.Errorf("Header %q: expected %q, got %q", header, expected, got)
			}
		}
	})

	t.Run("Next handler is called and response passes through", func(t *testing.T) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		handler := SecurityHeaders(next)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusTeapot {
			t.Errorf("Expected status %d, got %d", http.StatusTeapot, rec.Code)
		}
	})
}