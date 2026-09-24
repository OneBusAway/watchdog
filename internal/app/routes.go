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

	// TODO(architecture): Historical investigation of unmatched stops and trips is
	// not currently reliable. The upstream /api/where/metrics.json endpoint exposes
	// only its current in-memory snapshot, and Watchdog does not retain authoritative
	// observation history across restarts. The existing unmatched-stop Prometheus
	// series remains present for 24 hours after its last observation, so it represents
	// retained state rather than the exact time an entity was unmatched. Adding an
	// API over the current tracker would therefore expose only incomplete current
	// state, not answer questions about previous hours or days.
	//
	// The challenge is to retain entity-level history without creating excessive
	// Prometheus cardinality or imposing unsuitable infrastructure on open-source
	// deployments. The architecture must decide between Prometheus, embedded SQLite,
	// and an external event-oriented store; define snapshots versus events or
	// coalesced episodes; establish retention, restart, backup, and pruning behavior;
	// account for single and multiple Watchdog replicas; handle historical stop
	// metadata when GTFS changes; and define how authenticated, filtered, paginated
	// investigation queries access the selected historical source.

	// Wrap router with Sentry and SecurityHeaders middlewares
	// Return wrapped httprouter instance.
	handler := middleware.SentryMiddleware(router)
	return middleware.SecurityHeaders(handler)
}
