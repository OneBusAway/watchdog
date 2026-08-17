package app

import (
	"net/http"
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