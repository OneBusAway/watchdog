package middleware

import (
	"net/http"
	"time"

	"github.com/getsentry/sentry-go/http"
)

// SentryMiddleware wraps the provided HTTP handler with Sentry's error tracking middleware.
// It captures and reports panics or errors to Sentry, waits up to 2 seconds for event delivery,
// and re-panics after reporting (so the error can still be logged or handled upstream).
func SentryMiddleware(next http.Handler) http.Handler {
	sentryHandler := sentryhttp.New(sentryhttp.Options{
		Repanic:         true,
		WaitForDelivery: true,
		Timeout:         2 * time.Second,
	})

	return sentryHandler.Handle(next)
}
