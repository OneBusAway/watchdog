package config

import (
	"io"
	"log/slog"
	"net/http"
)

type mockRoundTripper struct {
	calls   int
	handler func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls++
	return m.handler(req)
}

// testLogger returns a logger that discards output so config-loading tests
// don't pollute test output but still exercise the logging paths.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
