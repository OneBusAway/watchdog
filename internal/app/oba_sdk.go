package app

import (
	"net/http"
	"sync"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/models"
)

// obaSDKKey uniquely identifies an OBA server by its base URL and API key.
// Two servers that share a base URL but use different keys must get separate
// clients, otherwise one server's key would be sent on the other's requests.
type obaSDKKey struct {
	baseURL string
	apiKey  string
}

// ObaSDKClientCache builds and reuses one OneBusAway SDK client per
// (base URL, API key) pair.
//
// A *onebusaway.Client allocates its own http.Transport and connection pool.
// Constructing one on every collection tick would discard pooled connections
// and force repeated TCP/TLS handshakes. Reusing a single client per server
// keeps connections warm across polls (IdleConnTimeout 90s > 30s tick).
//
// Clients are created with the injected pooled *http.Client via
// option.WithHTTPClient, so SDK requests:
//   - reuse connections alongside watchdog's own traffic,
//   - inherit the pooled client's timeouts,
//   - are captured by http_outgoing_request_duration_seconds
//     (latencyTrackingRoundTripper).
type ObaSDKClientCache struct {
	client  *http.Client
	mu      sync.Mutex
	clients map[obaSDKKey]*onebusaway.Client
}

// NewObaSDKClientCache returns a cache that builds clients on top of the given
// pooled HTTP client.
func NewObaSDKClientCache(client *http.Client) *ObaSDKClientCache {
	return &ObaSDKClientCache{
		client:  client,
		clients: make(map[obaSDKKey]*onebusaway.Client),
	}
}

// For returns the SDK client for the given server, creating and caching it on
// first use. Safe for concurrent use.
func (c *ObaSDKClientCache) For(server models.ObaServer) *onebusaway.Client {
	key := obaSDKKey{baseURL: server.ObaBaseURL, apiKey: server.ObaApiKey}

	c.mu.Lock()
	defer c.mu.Unlock()

	if client, ok := c.clients[key]; ok {
		return client
	}

	opts := []option.RequestOption{
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	}
	if c.client != nil {
		opts = append(opts, option.WithHTTPClient(c.client))
	}

	client := onebusaway.NewClient(opts...)
	c.clients[key] = client
	return client
}
