package gtfs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type backendHashAttempt struct {
	fingerprint string
	hashes      []string
}

// CheckBackendBundles checks one deployment, including any agency-scoped entries
// sharing its URL. Metadata is optional and independent of API health checks.
// campaignCtx outlives the collection deadline so refresh retries can finish.
func (gs *GtfsService) CheckBackendBundles(ctx, campaignCtx context.Context, servers []models.ObaServer) {
	if len(servers) == 0 {
		return
	}

	eligible := backendHashEligible(servers)
	hash := ""
	if eligible {
		hash = gs.fetchBackendHash(ctx, servers[0])
	}
	// A gauge describes a deployment, so aggregate siblings before emitting
	// once per label set. An unknown local snapshot makes the result unknown.
	results := make(map[string]*bool)
	representatives := make(map[string]models.ObaServer)
	for _, server := range servers {
		key := server.ServerName
		representatives[key] = server
		var match *bool
		if eligible && hash != "" && gs.StaticStore.IsConfigured(server) {
			c := gs.refreshCoordinator
			fingerprint := staticConfigFingerprint(server)
			gs.StaticStore.mu.RLock()
			if !gs.StaticStore.isConfiguredLocked(server) {
				gs.StaticStore.mu.RUnlock()
				continue
			}
			c.mu.Lock()
			content, ok := c.snapshotContent[server.ServerKey()]
			if ok && content.fingerprint == fingerprint && len(content.hashes) == 1 {
				value := content.hashes[0] == hash
				match = &value
			}
			c.mu.Unlock()
			gs.StaticStore.mu.RUnlock()
			if match == nil || !*match {
				if c.start(campaignCtx, gs, server, StaticRefreshBackendChanged, 5, hash) {
					gs.Logger.Info("Starting backend-changed static GTFS refresh campaign", "server_name", server.ServerName,
						"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "trigger", StaticRefreshBackendChanged)
				}
			}
		}
		prior, seen := results[key]
		if !seen {
			results[key] = match
		} else if prior == nil || match == nil {
			results[key] = nil
		} else {
			value := *prior && *match
			results[key] = &value
		}
	}
	for key, match := range results {
		gs.observeBackendHash(representatives[key], match)
	}
}

// backendHashEligible reports whether all entries for a backend
// refer to the same single zip. Per-agency zips and mixed feed configurations
// cannot describe the identity of the backend bundle as a whole.
func backendHashEligible(servers []models.ObaServer) bool {
	if len(servers) == 0 || len(servers[0].GtfsStaticFeeds) != 1 {
		return false
	}
	feed := servers[0].GtfsStaticFeeds[0]
	for _, server := range servers {
		if len(server.GtfsStaticFeeds) != 1 || server.GtfsStaticFeeds[0] != feed {
			return false
		}
	}
	return true
}

// observeBackendHash reports a comparison only while its static configuration remains current.
func (gs *GtfsService) observeBackendHash(server models.ObaServer, match *bool) {
	// Collection must not wait for the potentially long publication lock.
	// As with other collection gauges, departed-server pruning retires a
	// series recreated by an in-flight tick after configuration removal.
	if gs.StaticStore.IsConfigured(server) && gs.BackendHashObserver != nil {
		gs.BackendHashObserver(server, match)
	}
}

// fetchBackendHash returns a valid SHA-256 from optional backend metadata, or an empty string on failure.
func (gs *GtfsService) fetchBackendHash(ctx context.Context, server models.ObaServer) string {
	// A stalled optional endpoint must not consume the collection deadline.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(server.ObaBaseURL, "/") + "/api/v2/metadata.json")
	if err != nil {
		return ""
	}
	query := endpoint.Query()
	query.Set("key", server.ObaApiKey)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return ""
	}
	response, err := gs.Client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			gs.logBackendMetadataFailure(server, "request_failed")
		}
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusServiceUnavailable {
			gs.Logger.Debug("Backend metadata request returned unexpected status", "server_name", server.ServerName,
				"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "status", response.StatusCode)
		}
		return ""
	}
	var metadata struct {
		StaticGtfsHash string `json:"staticGtfsHash"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata) != nil {
		if ctx.Err() == nil {
			gs.logBackendMetadataFailure(server, "malformed_json")
		}
		return ""
	}
	hash := strings.ToLower(strings.TrimSpace(metadata.StaticGtfsHash))
	// An absent hash is an expected unsupported-backend response.
	if hash == "" {
		return ""
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 {
		gs.logBackendMetadataFailure(server, "invalid_hash")
		return ""
	}
	return hash
}

// logBackendMetadataFailure records optional metadata failures without exposing response bodies or API keys.
func (gs *GtfsService) logBackendMetadataFailure(server models.ObaServer, reason string) {
	gs.Logger.Debug("Backend metadata unavailable", "server_name", server.ServerName,
		"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "reason", reason)
}
