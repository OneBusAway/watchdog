package gtfs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
}

// stable reports whether the run has lasted long enough to act on, rather than
// being one node's answer behind a load balancer.
func (r backendHashRun) stable() bool {
	return r.ticks >= backendHashStableTicks && r.lastSeen.Sub(r.firstSeen) >= backendHashStableFor
}

// backendCampaignKey identifies one mismatch: a configured entry, its static
// configuration, the hash its backend reported, and the local snapshot hash.
type backendCampaignKey struct {
	server, fingerprint, hash, localHash string
}

// backendCampaign is the last backend_changed campaign started for a mismatch.
// It lives outside the run, so a run restarted by nodes answering with
// different hashes or by a metadata outage keeps the hourly retry limit.
type backendCampaign struct {
	at time.Time
	// warned records that the mismatch after that campaign was logged.
	warned bool
}

// BackendHashStatus is the outcome of one backend metadata check. The caller
// uses it to decide how long to wait before checking the deployment again and
// whether the backend hash currently covers the deployment's static bundle.
type BackendHashStatus int

const (
	// BackendHashNotChecked means no usable request was made: the entries are
	// not eligible for a hash comparison, or the check was canceled. It says
	// nothing about the backend, so it neither backs off nor covers the bundle.
	BackendHashNotChecked BackendHashStatus = iota
	// BackendHashOK means the backend reported a valid hash.
	BackendHashOK
	// BackendHashTransient means the request failed in a way that may clear up
	// on its own: a network error, a timeout, or a server error.
	BackendHashTransient
	// BackendHashUnsupported means the backend answered but exposes no usable
	// hash: the endpoint is missing or refused, or the body has no valid hash.
	BackendHashUnsupported
)

func (s BackendHashStatus) String() string {
	switch s {
	case BackendHashOK:
		return "ok"
	case BackendHashTransient:
		return "transient"
	case BackendHashUnsupported:
		return "unsupported"
	default:
		return "not_checked"
	}
}

// CheckBackendBundles checks one deployment, including any agency-scoped entries
// sharing its URL. Metadata is optional and independent of API health checks.
// campaignCtx outlives the collection deadline so refresh retries can finish.
func (gs *GtfsService) CheckBackendBundles(ctx, campaignCtx context.Context, servers []models.ObaServer) BackendHashStatus {
	if len(servers) == 0 {
		return BackendHashNotChecked
	}

	eligible := backendHashEligible(servers)
	hash := ""
	status := BackendHashNotChecked
	if eligible {
		hash, status = gs.fetchBackendHash(ctx, servers[0])
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
			relation := BackendBundleUnknown
			if ok && content.fingerprint == fingerprint {
				localHash = content.hash
				value := localHash == hash
				match = &value
				switch {
				case value:
					relation = BackendBundleMatch
					// The backend caught up. From now on, the replaced bundle's
					// hash means a rollback, which is a real change to follow.
					delete(c.superseded, server.ServerKey())
				case c.supersedesLocked(server.ServerKey(), fingerprint, hash):
					relation = BackendBundleBehind
				default:
					relation = BackendBundleDiffers
				}
			}
			run, startCampaign, warn := c.observeBackendRunLocked(server, fingerprint, hash, localHash, match)
			published := relation
			debouncing := false
			if relation == BackendBundleBehind {
				// One read can come from a single lagging node behind a load
				// balancer, so report the backend as behind only once the
				// stale hash has held. A confirmed state survives a metadata
				// gap, which restarts the run but is no evidence of a catch-up.
				if !run.stable() && !c.relationIsLocked(server.ServerKey(), fingerprint, BackendBundleBehind) {
					published = BackendBundleUnknown
					debouncing = true
				}
				// Watchdog already replaced the bundle the backend serves, so
				// downloading the feed now would fetch the same newer bundle
				// again: wait for the backend to load it. The hourly retry
				// still runs, because the newer bundle may have been a mistake
				// that the agency has since reverted on the feed URL.
				campaignKey := backendCampaignKey{server.ServerKey(), fingerprint, hash, localHash}
				if startCampaign && c.backendCampaigns[campaignKey].at.IsZero() {
					c.backendCampaigns[campaignKey] = backendCampaign{at: run.lastSeen}
					startCampaign = false
				}
				warn = false
			}
			relationChanged := c.setRelationLocked(server.ServerKey(), fingerprint, published, debouncing)
			c.mu.Unlock()
			gs.StaticStore.mu.RUnlock()
			if match != nil && *match {
				// The backend serves the bundle Watchdog holds, so the
				// snapshot is confirmed current without downloading it.
				gs.StaticStore.TouchServerSnapshot(server, time.Now().UTC())
			}
			if relationChanged && published == BackendBundleBehind {
				gs.Logger.Warn("Backend still serves the static GTFS bundle Watchdog replaced; waiting for it to load the newer one",
					"server_name", server.ServerName, "server_url", utils.SanitizeServerURL(server.ObaBaseURL),
					"agency_id", server.AgencyID, "agency_name", server.AgencyName, "backend_hash", hash, "local_hash", localHash)
			}
			if warn {
				gs.Logger.Warn("Backend static GTFS bundle still differs from the configured feed after refresh",
					"server_name", server.ServerName, "server_url", utils.SanitizeServerURL(server.ObaBaseURL),
					"feed_url", utils.SanitizeServerURL(server.GtfsStaticFeeds[0]), "agency_id", server.AgencyID,
					"agency_name", server.AgencyName, "backend_hash", hash, "local_hash", localHash)
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
	return status
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
	if now.Sub(run.firstSeen) >= backendHashSettledFor {
		for campaignKey := range c.backendCampaigns {
			if campaignKey.server == key && campaignKey.hash != hash {
				delete(c.backendCampaigns, campaignKey)
			}
		}
	}
	startCampaign, warn := false, false
	if match == nil || !*match {
		_, active := c.active[key]
		campaignKey := backendCampaignKey{key, fingerprint, hash, localHash}
		campaign := c.backendCampaigns[campaignKey]
		if match != nil && !campaign.at.IsZero() && !campaign.warned && !active {
			campaign.warned, warn = true, true
			c.backendCampaigns[campaignKey] = campaign
		}
		startCampaign = run.stable() && (campaign.at.IsZero() || now.Sub(campaign.at) >= backendHashRetryEvery)
	}
	c.backendRuns[key] = run
	return run, startCampaign, warn
}

// markBackendCampaign records a started campaign for the mismatch its run
// observed, unless the entry's configuration changed in the meantime.
func (c *staticRefreshCoordinator) markBackendCampaign(server models.ObaServer, run backendHashRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := server.ServerKey()
	if current, ok := c.backendRuns[key]; !ok || current.fingerprint != run.fingerprint {
		return
	}
	// A campaign older than the retry interval no longer limits anything.
	for campaignKey, campaign := range c.backendCampaigns {
		if run.lastSeen.Sub(campaign.at) >= backendHashRetryEvery {
			delete(c.backendCampaigns, campaignKey)
		}
	}
	c.backendCampaigns[backendCampaignKey{key, run.fingerprint, run.hash, run.localHash}] = backendCampaign{at: run.lastSeen}
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

// fetchBackendHash returns a valid SHA-256 from optional backend metadata and
// the status of the request. The hash is empty unless the status is OK.
func (gs *GtfsService) fetchBackendHash(ctx context.Context, server models.ObaServer) (string, BackendHashStatus) {
	// A stalled optional endpoint must not consume the collection deadline.
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, backendMetadataTimeout)
	defer cancel()
	endpoint, err := url.Parse(strings.TrimRight(server.ObaBaseURL, "/") + "/api/v2/metadata.json")
	if err != nil {
		gs.logBackendMetadataFailure(server, "invalid_url")
		return "", BackendHashUnsupported
	}
	query := endpoint.Query()
	query.Set("key", server.ObaApiKey)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		gs.logBackendMetadataFailure(server, "invalid_url")
		return "", BackendHashUnsupported
	}
	response, err := gs.Client.Do(req)
	if err != nil {
		if callerCtx.Err() != nil {
			// The cycle ended or shutdown began: not the backend's fault.
			return "", BackendHashNotChecked
		}
		gs.logBackendMetadataFailure(server, "request_failed")
		return "", BackendHashTransient
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 404 and 503 are how backends without metadata ordinarily answer.
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusServiceUnavailable {
			gs.Logger.Debug("Backend metadata request returned unexpected status", "server_name", server.ServerName,
				"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "status", response.StatusCode)
		}
		return "", classifyBackendMetadataStatus(response.StatusCode)
	}
	var metadata struct {
		StaticGtfsHash string `json:"staticGtfsHash"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata); err != nil {
		if callerCtx.Err() != nil {
			return "", BackendHashNotChecked
		}
		if !isJSONContentError(err) {
			// The body stopped arriving (the request deadline, a reset
			// connection), which may clear up on its own, unlike a backend
			// that answers with something that is not a metadata document.
			gs.logBackendMetadataFailure(server, "body_read_failed")
			return "", BackendHashTransient
		}
		gs.logBackendMetadataFailure(server, "malformed_json")
		return "", BackendHashUnsupported
	}
	hash := strings.ToLower(strings.TrimSpace(metadata.StaticGtfsHash))
	if hash == "" {
		// An absent hash is how an older backend ordinarily answers.
		return "", BackendHashUnsupported
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 {
		gs.logBackendMetadataFailure(server, "invalid_hash")
		return "", BackendHashUnsupported
	}
	return hash, BackendHashOK
}

// backendMetadataTimeout bounds one metadata request, body included. A
// variable so tests need not wait for it.
var backendMetadataTimeout = 2 * time.Second

// isJSONContentError reports whether a decode error comes from the body's
// content (not JSON, or the wrong shape) rather than from reading it.
func isJSONContentError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// classifyBackendMetadataStatus maps a non-OK HTTP status to a check result.
// Server errors and throttling may clear up soon. Anything else (a missing
// endpoint, a refused key) will not change until someone changes the backend.
func classifyBackendMetadataStatus(code int) BackendHashStatus {
	if code >= 500 || code == http.StatusTooManyRequests || code == http.StatusRequestTimeout {
		return BackendHashTransient
	}
	return BackendHashUnsupported
}

// logBackendMetadataFailure records optional metadata failures without exposing response bodies or API keys.
func (gs *GtfsService) logBackendMetadataFailure(server models.ObaServer, reason string) {
	gs.Logger.Debug("Backend metadata unavailable", "server_name", server.ServerName,
		"server_url", utils.SanitizeServerURL(server.ObaBaseURL), "reason", reason)
}
