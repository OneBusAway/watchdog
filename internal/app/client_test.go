package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPooledClient(t *testing.T) {
	client := NewPooledClient()
	if client == nil {
		t.Fatalf("client is nil")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/testpath", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}

func TestLatencyTrackingRoundTripperError(t *testing.T) {
	client := NewPooledClient()
	
	// Create a request to a non-existent server to trigger an error
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:0/fail", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	_, err = client.Do(req)
	if err == nil {
		t.Fatalf("expected error for non-existent server")
	}
}
