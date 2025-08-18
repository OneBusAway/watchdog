package middleware

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type CachedPromHandler struct {
	mu    sync.RWMutex
	cache []byte
	ttl   time.Duration
	h     http.Handler
}

func NewCachedPromHandler(ctx context.Context, gatherer prometheus.Gatherer, ttl time.Duration) *CachedPromHandler {
	c := &CachedPromHandler{
		ttl: ttl,
		h:   promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}),
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
			c.h.ServeHTTP(rec, nil)

			c.mu.Lock()
			c.cache = buf.Bytes()
			c.mu.Unlock()
		}
	}
}

func (c *CachedPromHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// If cache is empty (very early after startup), fall back to live handler.
	if len(c.cache) == 0 {
		c.h.ServeHTTP(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write(c.cache)
}

type responseRecorder struct {
	buf *bytes.Buffer
}

func (rr *responseRecorder) Header() http.Header         { return http.Header{} }
func (rr *responseRecorder) Write(b []byte) (int, error) { return rr.buf.Write(b) }
func (rr *responseRecorder) WriteHeader(statusCode int)  {}
