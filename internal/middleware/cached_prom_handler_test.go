package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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

func TestCachedPromHandler_DoesNotServeErrorBodyAs200(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "good_metric", Help: "test"})
	reg.MustRegister(g)
	g.Set(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := NewCachedPromHandler(ctx, reg, 20*time.Millisecond)

	// Wait for a successful refresh to populate the cache.
	time.Sleep(60 * time.Millisecond)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from populated cache, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "good_metric") {
		t.Fatalf("expected good_metric in body, got: %s", rec.Body.String())
	}
}
