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

// toggleGatherer is a Gatherer that returns a valid metric while ok is true,
// and returns an error with no families once ok is set to false.
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
//   - While the gatherer succeeds, the cached body is served with 200.
//   - After the gatherer fails, the cache is cleared so the next scrape falls
//     through to the live handler and returns a non-200 — never the error body
//     with a 200 status.
func TestCachedPromHandler_ClearsOnGatherFailure(t *testing.T) {
	tg := newToggleGatherer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const ttl = 20 * time.Millisecond
	h := NewCachedPromHandler(ctx, tg, ttl, nopLogger())

	// Poll until the cache is warm with a successful gather.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "good_metric") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	warm := httptest.NewRecorder()
	h.ServeHTTP(warm, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if warm.Code != http.StatusOK || !strings.Contains(warm.Body.String(), "good_metric") {
		t.Fatalf("cache not populated after 2s: status=%d body=%s", warm.Code, warm.Body)
	}

	// Flip the gatherer to fail, then wait until the cache is cleared.
	tg.ok.Store(false)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			// Cache was cleared; live handler returned a non-200 error.
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cache was not cleared after gather failure within 2s")
}
