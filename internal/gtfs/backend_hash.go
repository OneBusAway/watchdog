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

// backendHashRun is the hash a backend has reported on consecutive
// observations for one configured entry, together with the static
// configuration and local snapshot hash it was compared against. A change in
// any of these, or a gap between observations longer than the stable duration
// or three fetch intervals (whichever is longer), starts a new run.
type backendHashRun struct {
	fingerprint string
	hash        string
	localHash   string
	firstSeen   time.Time
	lastSeen    time.Time
	ticks       int
	// campaignAt is when this run last started a backend_changed campaign.
	campaignAt time.Time
	// warned records that the mismatch after that campaign was logged.
	warned bool
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
			localHash := ""
			if ok && content.fingerprint == fingerprint && len(content.hashes) == 1 {
				localHash = content.hashes[0]
				value := localHash == hash
				match = &value
			}
			run, startCampaign, warn := c.observeBackendRunLocked(server, fingerprint, hash, localHash, match)
			c.mu.Unlock()
			gs.StaticStore.mu.RUnlock()
			if warn {
				gs.Logger.Warn("Backend static GTFS bundle still differs from the configured feed after refresh",
					"server_name", server.ServerName, "server_url", utils.SanitizeServerURL(server.ObaBaseURL),
					"feed_url", utils.SanitizeServerURL(server.GtfsStaticFeeds[0]), "agency_id", server.AgencyID,
					"backend_hash", hash, "local_hash", localHash)
			}
			if startCampaign && c.start(campaignCtx, gs, server, StaticRefreshBackendChanged, 5) {
				c.markBackendCampaign(server, run)
				gs.Logger.Info("Starting backend-changed static GTFS refresh campaign", "server_name", server.ServerName,
					"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "trigger", StaticRefreshBackendChanged, "agency_id", server.AgencyID, "agency_name", server.AgencyName)
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

// observeBackendRunLocked records one backend hash observation and decides
// whether it starts a campaign or logs that a finished campaign left the
// backend and the configured feed disagreeing. c.mu must be held.
func (c *staticRefreshCoordinator) observeBackendRunLocked(server models.ObaServer, fingerprint, hash, localHash string, match *bool) (backendHashRun, bool, bool) {
	key := server.ServerKey()
	now := c.now()
	run := c.backendRuns[key]
	// Tolerate a couple of missed ticks, but never less than the stable
	// duration, so a fetch interval longer than it can still form a run.
	maxGap := max(backendHashStableFor, backendHashStableTicks*c.checkInterval)
	if run.fingerprint != fingerprint || run.hash != hash || run.localHash != localHash || now.Sub(run.lastSeen) > maxGap {
		// Without continuous observations, a hash seen before and after a gap
		// says nothing about what the backend reported in between.
		run = backendHashRun{fingerprint: fingerprint, hash: hash, localHash: localHash, firstSeen: now}
	}
	run.lastSeen = now
	run.ticks++
	startCampaign, warn := false, false
	if match == nil || !*match {
		_, active := c.active[key]
		if match != nil && !run.campaignAt.IsZero() && !run.warned && !active {
			run.warned, warn = true, true
		}
		startCampaign = run.ticks >= backendHashStableTicks && now.Sub(run.firstSeen) >= backendHashStableFor &&
			(run.campaignAt.IsZero() || now.Sub(run.campaignAt) >= backendHashRetryEvery)
	}
	c.backendRuns[key] = run
	return run, startCampaign, warn
}

// markBackendCampaign records a started campaign on the run that requested it, if that run is still current.
func (c *staticRefreshCoordinator) markBackendCampaign(server models.ObaServer, run backendHashRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := server.ServerKey()
	current, ok := c.backendRuns[key]
	if !ok || current.fingerprint != run.fingerprint || current.hash != run.hash || current.localHash != run.localHash || !current.firstSeen.Equal(run.firstSeen) {
		return
	}
	current.campaignAt, current.warned = run.lastSeen, false
	c.backendRuns[key] = current
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
	callerCtx := ctx
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
		if callerCtx.Err() == nil {
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
		if callerCtx.Err() == nil {
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
