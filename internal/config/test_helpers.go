package config

import "net/http"

type mockRoundTripper struct {
	calls   int
	handler func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls++
	return m.handler(req)
}