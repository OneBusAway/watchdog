package middleware

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"watchdog.onebusaway.org/internal/report"
)

// CachedPromHandler wraps promhttp.HandlerFor with a caching layer.
// It precomputes the Prometheus exposition at fixed intervals and serves the
// last good result. When a gather fails the cache is cleared so the next
// scrape falls through to the live handler and Prometheus sees the real 500,
// keeping the failure visible rather than freezing all gauges at their last
// value indefinitely.
type CachedPromHandler struct {
	mu      sync.RWMutex
	cache   []byte
	failing bool // true once the first failing refresh is seen; reset on success
	ttl     time.Duration
	h       http.Handler
	log     *slog.Logger
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
			// A minimal synthetic request is required: promhttp.HandlerFor
			// dereferences Method and URL and panics on nil.
			req := &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/metrics"},
				Header: make(http.Header),
			}
			c.h.ServeHTTP(rec, req)

			c.mu.Lock()
			// status == 0 means promhttp gathered successfully but produced no
			// metric families, so it never called Write or WriteHeader.
			if rec.status == 0 || rec.status == http.StatusOK {
				c.cache = buf.Bytes()
				c.failing = false
			} else {
				// Clear the cache so ServeHTTP falls back to the live handler.
				// Prometheus then sees a real 500 and up drops to 0, keeping
				// the failure visible.
				errMsg := strings.TrimSpace(buf.String())
				c.log.Error("metrics gather failed; clearing cache so scraper sees the error",
					"status", rec.status, "error", errMsg)
				// Report only on the first failing refresh to avoid flooding Sentry.
				if !c.failing {
					report.ReportErrorWithSentryOptions(
						fmt.Errorf("metrics gather failed (status %d): %s", rec.status, errMsg),
						report.SentryReportOptions{},
					)
					c.failing = true
				}
				c.cache = nil
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
	// Use the Prometheus-provided constant for the text exposition Content-Type
	// to stay aligned with the official library and avoid deprecated constants.
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
