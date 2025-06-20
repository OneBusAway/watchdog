package middleware

import (
	"net/http"
	"time"

	"github.com/getsentry/sentry-go/http"
)

func SentryMiddleware(next http.Handler) http.Handler {
	sentryHandler := sentryhttp.New(sentryhttp.Options{
		Repanic:         true,
		WaitForDelivery: true,
		Timeout:         2 * time.Second,
	})

	return sentryHandler.Handle(next)
}
