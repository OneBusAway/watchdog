package middleware

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
)

func nopLogger() *slog.Logger { return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)) }

func TestResponseRecorder_TracksStatusCode(t *testing.T) {
	rr := &responseRecorder{buf: new(bytes.Buffer)}
	rr.WriteHeader(http.StatusInternalServerError)
	if rr.status != http.StatusInternalServerError {
		t.Fatalf("got status %d, want %d", rr.status, http.StatusInternalServerError)
	}
}

func TestResponseRecorder_ImpliesOKOnWrite(t *testing.T) {
	rr := &responseRecorder{buf: new(bytes.Buffer)}
	rr.Write([]byte("hello"))
	if rr.status != http.StatusOK {
		t.Fatalf("got status %d, want %d", rr.status, http.StatusOK)
	}
}

// toggleGatherer returns a valid metric while ok is true, and returns an
// error with no families once ok is set to false.
type toggleGatherer struct {
	ok    atomic.Bool
	gauge prometheus.Gauge
}

func newToggleGatherer() *toggleGatherer {
	g := &toggleGatherer{}
	g.ok.Store(true)
	g.gauge = prometheus.NewGauge(prometheus.GaugeOpts{Name: "good_metric", Help: "test"})
	g.gauge.Set(1)
	return g
}

func (g *toggleGatherer) Gather() ([]*dto.MetricFamily, error) {
	if !g.ok.Load() {
		return nil, fmt.Errorf("forced gather failure")
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(g.gauge)
	return reg.Gather()
}

// TestCachedPromHandler_ClearsOnGatherFailure verifies that:
//   - After a successful refresh the internal cache contains the metric.
//   - After the gatherer fails the cache is set to nil, so ServeHTTP falls
//     through to the live handler and returns a non-200 status.
//
// The test reads h.cache directly under h.mu so it exercises the cache logic
// rather than relying on responses that could come from the live handler before
// any refresh tick has run.
func TestCachedPromHandler_ClearsOnGatherFailure(t *testing.T) {
	tg := newToggleGatherer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const ttl = 20 * time.Millisecond
	h := NewCachedPromHandler(ctx, tg, ttl, nopLogger())

	// Wait until the refresh loop has stored the metric in the cache.
	deadline := time.Now().Add(2 * time.Second)
	cacheWarm := false
	for time.Now().Before(deadline) {
		h.mu.RLock()
		cacheWarm = strings.Contains(string(h.cache), "good_metric")
		h.mu.RUnlock()
		if cacheWarm {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cacheWarm {
		t.Fatal("cache not populated after 2s")
	}
	warm := httptest.NewRecorder()
	h.ServeHTTP(warm, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
	if warm.Code != http.StatusOK || !strings.Contains(warm.Body.String(), "good_metric") {
		t.Fatalf("warm cache: status=%d body=%s", warm.Code, warm.Body)
	}

	// Flip the gatherer to fail, then wait until the cache is cleared.
	tg.ok.Store(false)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.RLock()
		cleared := len(h.cache) == 0
		h.mu.RUnlock()
		if cleared {
			// Cache was cleared; the live handler must not return 200.
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
			if rec.Code == http.StatusOK {
				t.Fatalf("live handler returned 200 after gather failure: body=%s", rec.Body)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cache was not cleared after gather failure within 2s")
}
