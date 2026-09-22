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
// This function initializes a new `httprouter.Router`, registers all routes with their handlers,
// and wraps the router with Sentry and security middleware.
//
// Parameters:
//   - ctx: Carries deadlines/cancellation and is passed to the cached Prometheus handler.
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
//   - middleware.SentryMiddleware:
//     Captures panics/errors and reports them to Sentry with request context.
//   - middleware.SecurityHeaders:
//     Adds HTTP security-related headers to every response.
//
// Purpose:
//   - Centralize route registration for modularity, testability, and a clear entry point
//     for all incoming HTTP traffic.
//   - Wire core middleware to ensure observability (Sentry, Prometheus) and baseline
//     HTTP security headers.
//   - Improve performance and reduce Prometheus scrape overhead through cached metrics.
//   - Support graceful cancellation and shutdown via ctx.
//
// Returns:
//   - An `http.Handler` instance that the server can use to handle incoming HTTP requests.

func (app *Application) Routes(ctx context.Context) http.Handler {
	// Initialize a new httprouter router instance.
	router := httprouter.New()

	// Register the relevant methods, URL patterns and handler functions for our
	// endpoints using the HandlerFunc() method. Note that http.MethodGet and
	// http.MethodPost are constants which equate to the strings "GET" and "POST"
	// respectively.
	router.HandlerFunc(http.MethodGet, "/v1/healthcheck", app.healthcheckHandler)
	router.Handler(http.MethodGet, "/metrics", middleware.NewCachedPromHandler(ctx, prometheus.DefaultGatherer, 10*time.Second))

	// TODO(investigation-api): Add authenticated, paginated JSON endpoints for
	// focused dashboard investigation, separate from Prometheus exposition:
	//   GET /v1/investigations/unmatched-stops
	//   GET /v1/investigations/unmatched-trips
	// Stop records should expose server/agency identity, stop ID/name,
	// coordinates, station/cluster membership, last_seen, and age_seconds.
	// Trip records should expose only authoritative diagnostics (trip/route ID,
	// service date, reason, last_seen); OBA currently reports aggregate trip
	// counts, so do not infer OBA failure reasons from an independent matcher.
	// Keep entity IDs and locations out of /metrics to avoid unbounded label
	// cardinality. Require service authentication, filters, bounded page sizes,
	// and observation timestamps so retained data cannot be mistaken for current.

	// Wrap router with Sentry and SecurityHeaders middlewares
	// Return wrapped httprouter instance.
	handler := middleware.SentryMiddleware(router)
	return middleware.SecurityHeaders(handler)
}
