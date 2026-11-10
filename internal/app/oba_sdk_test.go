package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"watchdog.onebusaway.org/internal/models"
)

func TestObaSDKClientCacheFor(t *testing.T) {
	cache := NewObaSDKClientCache(&http.Client{Timeout: 5 * time.Second})

	server := models.ObaServer{
		AgencyID:   "agency-a",
		ObaBaseURL: "https://api.example.com",
		ObaApiKey:  "key-a",
	}

	first := cache.For(server)
	second := cache.For(server)
	if first != second {
		t.Fatal("expected the same client to be reused for the same server")
	}
	if _, ok := interface{}(first).(*onebusaway.Client); !ok {
		t.Fatalf("expected a *onebusaway.Client, got %T", first)
	}
}

func TestObaSDKClientCacheKeying(t *testing.T) {
	cache := NewObaSDKClientCache(&http.Client{Timeout: 5 * time.Second})

	baseURL := "https://api.example.com"
	clientA := cache.For(models.ObaServer{ObaBaseURL: baseURL, ObaApiKey: "key-a"})
	clientB := cache.For(models.ObaServer{ObaBaseURL: baseURL, ObaApiKey: "key-b"})

	if clientA == clientB {
		t.Fatal("expected distinct clients for different API keys on the same base URL")
	}

	clientAAgain := cache.For(models.ObaServer{ObaBaseURL: baseURL, ObaApiKey: "key-a"})
	if clientAAgain != clientA {
		t.Fatal("expected key-a client to be reused after key-b was created")
	}
}

// TestObaSDKClientCacheDisablesRetries verifies that SDK clients built by the
// cache configure option.WithMaxRetries(0) so that a hanging step fails within
// a single request timeout rather than consuming ~3 timeouts in retries.
func TestObaSDKClientCacheDisablesRetries(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// Delay longer than the HTTP client timeout to simulate a hanging endpoint.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	clientTimeout := 150 * time.Millisecond
	cache := NewObaSDKClientCache(&http.Client{Timeout: clientTimeout})

	server := models.ObaServer{
		AgencyID:   "agency-test",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "test-key",
	}
	client := cache.For(server)

	start := time.Now()
	_, err := client.CurrentTime.Get(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected request timeout error, got nil")
	}

	// WithMaxRetries(0) ensures only 1 attempt is made (0 retries).
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected exactly 1 attempt (0 retries), got %d attempts", got)
	}

	// Must fail within roughly one request timeout (~150ms), well below the ~3
	// timeouts plus backoff (~2s+) that default retries (2) would take.
	if elapsed > 600*time.Millisecond {
		t.Fatalf("expected call to fail within one request timeout (~%v), took %v", clientTimeout, elapsed)
	}
}
