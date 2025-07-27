package app

import (
	"net"
	"net/http"
	"time"
)

// NewPooledClient returns an HTTP client optimized for polling APIs every 30 seconds,
// such as GTFS-RT endpoints in the Watchdog project.
//
// The transport configuration is tuned for:
//   - Efficient connection reuse to avoid repeated TCP/TLS handshakes
//   - Controlled timeouts to detect unresponsive servers without long hangs
//   - Sensible defaults where custom tuning is unnecessary
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

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	return client
}
