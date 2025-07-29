package app

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"watchdog.onebusaway.org/internal/metrics"
)

// latencyTrackingRoundTripper is a custom HTTP RoundTripper that wraps another RoundTripper
// to measure and record the latency (duration) of each outgoing HTTP request.
//
// Purpose:
// - Collect Prometheus metrics on request latency (in seconds)
// - Label the metrics by URL, HTTP method, and response status
// - Help monitor external API performance in systems like Watchdog
//
// Why use this:
// Prometheus doesn’t automatically track request latency. Wrapping the transport lets us
// measure latency without changing the logic of every API call.
type latencyTrackingRoundTripper struct {
	// next is the underlying RoundTripper that actually performs the request.
	// This allows us to insert instrumentation without changing request behavior.
	next http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface.
// It records the time before and after delegating to the next RoundTripper,
// then exports the observed duration to Prometheus under metrics.OutgoingLatency.
func (rt *latencyTrackingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.next.RoundTrip(req)
	duration := time.Since(start).Seconds()

	// Default to "error" if the request failed or response is nil
	status := "error"
	if err == nil && resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}

	// Construct a safe, normalized URL label (scheme + host + path) without query params
	safeURL := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path

	// Record latency with Prometheus, labeled by URL, HTTP method, and status
	metrics.OutgoingLatency.WithLabelValues(
		safeURL,
		req.Method,
		status,
	).Observe(duration)

	return resp, err
}

// NewPooledClient returns an HTTP client optimized for polling APIs every 30 seconds,
// such as GTFS-RT endpoints in the Watchdog project.
//
// The transport configuration is tuned for:
//   - Efficient connection reuse to avoid repeated TCP/TLS handshakes
//   - Controlled timeouts to detect unresponsive servers without long hangs
//   - Sensible defaults where custom tuning is unnecessary
//   - The client is also instrumented with Prometheus metrics to track request latency
//
// Configuration rationale:
//
//   - MaxIdleConns: 100
//     Allows up to 100 idle (keep-alive) connections across all hosts.
//     Suitable for multiple monitored servers; reduces connection churn.
//
//   - MaxIdleConnsPerHost: 10
//     Allows each API host to maintain up to 10 idle connections.
//     Helps when Watchdog queries many endpoints on the same host (e.g., GTFS feeds).
//
//   - IdleConnTimeout: 90s
//     Idle connections are kept for 90 seconds before being closed.
//     Since requests happen every 30 seconds, this ensures most connections stay alive.
//     Reduces cost of re-establishing TCP/TLS handshakes.
//
//   - DialContext (Timeout: 5s, KeepAlive: 30s):
//     Sets TCP connection timeout to 5s to fail fast if the server is unreachable.
//     TCP keep-alives are enabled to detect dead peers if connection remains open.
//
//   - TLSHandshakeTimeout: 5s
//     Caps the TLS handshake time. Prevents indefinite stalls during slow server negotiation.
//     Lower than default (10s) to reduce latency during degraded network conditions.
//
//   - http.Client Timeout: 10s
//     A global timeout covering the full request lifecycle (connect, TLS, redirect, read).
//     Ensures the system doesn't hang longer than necessary if the API is unresponsive.
//
// Latency Tracking:
//
//   - The client wraps its Transport with latencyTrackingRoundTripper.
//     This tracks the latency of outgoing HTTP requests using Prometheus histograms.
func NewPooledClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	instrumentedTransport := &latencyTrackingRoundTripper{next: transport}

	client := &http.Client{
		Transport: instrumentedTransport,
		Timeout:   10 * time.Second,
	}
	return client
}
