package app

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/middleware"

	"github.com/julienschmidt/httprouter"
)

// Routes sets up the HTTP routing configuration for the application and returns the final http.Handler.
//
// This function initializes a new `httprouter.Router`, registers all application routes
// with their corresponding handler functions and HTTP methods, and wraps the entire router
// with Sentry middleware for centralized error tracking and performance monitoring.
//
// Registered Routes:
//   - GET /v1/healthcheck:
//     Provides a JSON-formatted snapshot of the application's current health and readiness status.
//     Handled by `app.healthcheckHandler`.
//   - GET /metrics:
//     Exposes all Prometheus metrics collected by the application for scraping by Prometheus.
//     Handled by a cached Prometheus handler (`middleware.NewCachedPromHandler`), which
//     reduces collection overhead by caching exposition output for a configurable duration.
//
// Middleware:
//   - `middleware.SentryMiddleware`:
//     Wraps the router to automatically capture any panics, errors, or performance issues
//     and report them to Sentry with contextual request data.
//
// Purpose:
//   - Centralize route registration for modularity and testability.
//   - Establish a clear entry point for all incoming HTTP traffic.
//   - Ensure observability via Prometheus and Sentry integrations.
//   - Improve performance and reduce Prometheus scrape overhead through cached metrics.
//
// Returns:
//   - An `http.Handler` instance that the server can use to handle incoming HTTP requests.
//
// Usage:
//
//	Typically called during application startup and passed to `http.Server`:
//	  server := &http.Server{
//	      Addr:    ":4000",
//	      Handler: app.Routes(ctx),
//	  }
func (app *Application) Routes(ctx context.Context) http.Handler {
	// Initialize a new httprouter router instance.
	router := httprouter.New()

	// Register the relevant methods, URL patterns and handler functions for our
	// endpoints using the HandlerFunc() method. Note that http.MethodGet and
	// http.MethodPost are constants which equate to the strings "GET" and "POST"
	// respectively.
	router.HandlerFunc(http.MethodGet, "/v1/healthcheck", app.healthcheckHandler)
	router.Handler(http.MethodGet, "/metrics", middleware.NewCachedPromHandler(ctx, prometheus.DefaultGatherer, 10*time.Second))

	// Wrap router with Sentry and securityHeaders middlewares
	// Return wrapped httprouter instance.
	handler := middleware.SentryMiddleware(router)
	return middleware.SecurityHeaders(handler)
}
