package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// CachedPromHandler wraps promhttp.HandlerFor with a caching layer.
// It precomputes the Prometheus exposition at fixed intervals and serves the
// last good result. When a gather fails the cache is cleared so the next
// scrape falls through to the live handler and Prometheus sees the real 500,
// keeping the scrape failure visible rather than freezing all gauges at their
// last value.
type CachedPromHandler struct {
	mu    sync.RWMutex
	cache []byte
	ttl   time.Duration
	h     http.Handler
	log   *slog.Logger
}

// NewCachedPromHandler starts a background refresh loop and returns a handler
// that serves the cached exposition. log is used to record gather failures.
func NewCachedPromHandler(ctx context.Context, gatherer prometheus.Gatherer, ttl time.Duration, log *slog.Logger) *CachedPromHandler {
	c := &CachedPromHandler{
		ttl: ttl,
		h:   promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}),
		log: log,
	}
	go c.refreshLoop(ctx)
	return c
}

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
			req := &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/metrics"},
				Header: make(http.Header),
			}
			c.h.ServeHTTP(rec, req)

			c.mu.Lock()
			if rec.status == http.StatusOK {
				c.cache = buf.Bytes()
			} else {
				// Clear the cache so ServeHTTP falls back to the live handler.
				// This keeps the gather failure visible to Prometheus (the scrape
				// returns a 500 and up drops to 0) rather than freezing every gauge
				// at its last value indefinitely.
				c.cache = nil
				c.log.Error("metrics gather failed; clearing cache so scraper sees the error",
					"status", rec.status)
			}
			c.mu.Unlock()
		}
	}
}

// ServeHTTP serves the cached exposition, or falls back to the live handler
// when the cache is empty (startup or after a gather failure).
func (c *CachedPromHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.cache) == 0 {
		c.h.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	_, _ = w.Write(c.cache)
}

// responseRecorder captures the status code and body that promhttp writes.
// status is 0 until the first Write or WriteHeader call.
type responseRecorder struct {
	buf    *bytes.Buffer
	status int
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	return rr.buf.Write(b)
}

func (rr *responseRecorder) Header() http.Header        { return http.Header{} }
func (rr *responseRecorder) WriteHeader(statusCode int) { rr.status = statusCode }
