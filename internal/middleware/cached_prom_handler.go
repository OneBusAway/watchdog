package middleware

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// CachedPromHandler wraps promhttp.HandlerFor with a caching layer.
//
// Purpose:
//   - Prometheus scrapes `/metrics` endpoints frequently (every few seconds).
//   - Each scrape triggers metrics gathering and text serialization, which
//     can become expensive under high concurrency.
//   - CachedPromHandler precomputes the exposition at fixed intervals (ttl)
//     and serves that cached result to all clients.
//
// Benefit:
// - Reduces CPU and allocation overhead during high load.
// - Ensures predictable latency even if multiple Prometheus servers scrape at once.
type CachedPromHandler struct {
	mu    sync.RWMutex  // Guards concurrent access to cache
	cache []byte        // Holds the precomputed metrics exposition
	ttl   time.Duration // Refresh interval for the cache
	h     http.Handler  // Underlying promhttp handler used for actual gathering
}

// NewCachedPromHandler creates a new CachedPromHandler instance.
//
// Parameters:
// - ctx: a context passed down from main(), used to gracefully stop the background goroutine.
// - gatherer: the Prometheus gatherer (commonly prometheus.DefaultGatherer or a custom registry).
// - ttl: how often the cache should refresh; should be <= scrape interval.
//
// Why it exists:
// - Spawns a refreshLoop in the background to keep the cache warm.
// - Ensures clients always get a quick response without redoing expensive metric collection.
func NewCachedPromHandler(ctx context.Context, gatherer prometheus.Gatherer, ttl time.Duration) *CachedPromHandler {
	c := &CachedPromHandler{
		ttl: ttl,
		h:   promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}),
	}

	go c.refreshLoop(ctx)
	return c
}

// refreshLoop runs in a goroutine and periodically refreshes the metrics cache.
//
// Purpose:
// - Keeps the cached exposition up-to-date by calling promhttp.Handler.
// - Runs until the provided context is cancelled (e.g., application shutdown).
//
// Why context:
// - Without context, this goroutine would run forever, causing leaks.
// - Using ctx.Done() allows clean shutdown when the server stops.
func (c *CachedPromHandler) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var buf bytes.Buffer
			rec := &responseRecorder{buf: &buf}
			c.h.ServeHTTP(rec, nil)

			c.mu.Lock()
			c.cache = buf.Bytes()
			c.mu.Unlock()
		}
	}
}

// ServeHTTP implements http.Handler by serving cached metrics.
//
// Behavior:
//   - If cache is still empty (e.g., right after startup), falls back
//     to calling the underlying promhttp handler directly.
//   - Otherwise, serves the precomputed response immediately.
//
// Why it exists:
//   - Provides a standard http.Handler interface, so it can be plugged
//     directly into any HTTP server or router (mux, chi, etc.).
//   - Ensures consistent Content-Type for the Prometheus text exposition
//     format. Instead of hardcoding ("text/plain; version=0.0.4"), we use
//     expfmt.NewFormat(expfmt.TypeTextPlain) to stay aligned with the official
//     Prometheus library and avoid reliance on deprecated constants.
func (c *CachedPromHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// If cache is empty (very early after startup), fall back to live handler.
	if len(c.cache) == 0 {
		c.h.ServeHTTP(w, r)
		return
	}
	// Use Prometheus-provided constant for text exposition format (version=0.0.4)
	w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	_, _ = w.Write(c.cache)
}

// responseRecorder is a lightweight implementation of http.ResponseWriter.
//
// Purpose:
//   - promhttp.Handler writes directly to a ResponseWriter.
//   - To capture this output for caching, we need a fake ResponseWriter
//     that redirects writes into a bytes.Buffer instead of a socket.
//
// Why minimal:
//   - Only implements methods promhttp actually calls (Header, Write, WriteHeader).
//   - We ignore status codes because Prometheus metrics exposition
//     is always a `200 OK` if gathered successfully.
type responseRecorder struct {
	buf *bytes.Buffer
}

// Write appends the promhttp output into the buffer.
// Required to satisfy http.ResponseWriter interface.
func (rr *responseRecorder) Write(b []byte) (int, error) { return rr.buf.Write(b) }

// Header and WriteHeader are implemented only to satisfy the http.ResponseWriter interface.
// - Header returns a new, empty HTTP header map (unused).
// - WriteHeader is a no-op since status codes are not needed.
func (rr *responseRecorder) Header() http.Header        { return http.Header{} }
func (rr *responseRecorder) WriteHeader(statusCode int) {}
