package gtfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func staticConfigFingerprint(server models.ObaServer) string {
	data, _ := json.Marshal(struct {
		ServerName      string
		AgencyID        string
		AgencyName      string
		ObaBaseURL      string
		GtfsStaticFeeds []string
	}{server.ServerName, server.AgencyID, server.AgencyName, server.ObaBaseURL, server.GtfsStaticFeeds})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// errStaticServerUnconfigured is returned by publishStaticCampaign when the
// entry is no longer configured. It is terminal: retrying would only discard
// cached artifacts and re-download feeds for a snapshot that cannot publish.
var errStaticServerUnconfigured = errors.New("static refresh server is no longer configured")

// A backend hash starts a backend_changed campaign only once the backend has
// reported it steadily for both a minimum duration and a minimum number of
// observations, so a deployment in progress does not trigger downloads. A
// longer gap between observations restarts the run. While the same mismatch
// persists, the campaign is retried at most once per backendHashRetryEvery,
// even across runs. The mismatch is over once the backend has reported a
// different hash for backendHashSettledFor, which a load balancer picking
// nodes at random practically never produces.
const (
	backendHashStableFor   = 90 * time.Second
	backendHashStableTicks = 3
	backendHashRetryEvery  = time.Hour
	backendHashSettledFor  = 10 * time.Minute
)

const maxStaticCampaignCacheSize = int64(2 << 30)
const maxStaticFeedDownloadSize = int64(512 << 20)

type staticFeedFailureHistory struct {
	failureSince time.Time
	// failedEscalatingCampaigns counts failed campaigns whose trigger
	// escalates (see staticTriggerPolicy).
	failedEscalatingCampaigns int
	identicalFailures         int
	lastContentHash           string
	conservative              bool
	lastStage                 StaticFailureStage
	lastReason                StaticFailureReason
}

// publishedStaticContent is what a server's last successful publish was built
// from: its static configuration and the SHA-256 of the feed zip.
type publishedStaticContent struct {
	fingerprint string
	hash        string
}

type activeStaticCampaign struct {
	fingerprint string
	generation  uint64
	cancel      context.CancelFunc
}

// agencyMismatchState is the last backend/Watchdog agency mismatch that
// started a refresh for a server, under the static config it was observed with.
type agencyMismatchState struct {
	fingerprint string
	signature   string
}
type staticRefreshCoordinator struct {
	mu               sync.Mutex
	active           map[string]activeStaticCampaign
	histories        map[string]*staticFeedFailureHistory
	backendRuns      map[string]backendHashRun
	backendCampaigns map[backendCampaignKey]backendCampaign
	published        map[string]publishedStaticContent
	snapshotContent  map[string]publishedStaticContent
	mismatches       map[string]agencyMismatchState
	// superseded is, per server, the snapshot that its current snapshot
	// replaced, so a backend still serving it is recognised as behind.
	superseded map[string]publishedStaticContent
	// relations is, per server, how the backend's bundle related to the
	// snapshot at the last usable hash comparison.
	relations map[string]backendRelationState
	// feedChecks is, per server, the result of the last completed feed attempt.
	feedChecks     map[string]feedCheckState
	policy         staticRefreshRetryPolicy
	nextGeneration uint64
	now            func() time.Time
	// checkInterval is the collection fetch interval, which sets how long a
	// gap between backend hash observations may be before a run restarts.
	checkInterval time.Duration
}

// newStaticRefreshCoordinator initializes campaign, published-content, and backend-run tracking.
func newStaticRefreshCoordinator() *staticRefreshCoordinator {
	return &staticRefreshCoordinator{
		snapshotContent:  make(map[string]publishedStaticContent),
		backendRuns:      make(map[string]backendHashRun),
		backendCampaigns: make(map[backendCampaignKey]backendCampaign),
		active:           make(map[string]activeStaticCampaign),
		histories:        make(map[string]*staticFeedFailureHistory),
		published:        make(map[string]publishedStaticContent),
		mismatches:       make(map[string]agencyMismatchState),
		superseded:       make(map[string]publishedStaticContent),
		relations:        make(map[string]backendRelationState),
		feedChecks:       make(map[string]feedCheckState),
		policy:           defaultStaticRefreshRetryPolicy,
		now:              time.Now,
		checkInterval:    30 * time.Second,
	}
}

// staticFeedURL returns the entry's upstream-merged static feed. ok is false
// for an entry that does not configure exactly one, which config validation
// rejects.
func staticFeedURL(server models.ObaServer) (string, bool) {
	if len(server.GtfsStaticFeeds) != 1 {
		return "", false
	}
	return server.GtfsStaticFeeds[0], true
}

func staticFeedHistoryKey(server models.ObaServer, feedURL string) string {
	return server.ServerKey() + "|" + feedURL
}

// start registers a campaign only for current configuration, suppressing a second active campaign.
func (c *staticRefreshCoordinator) start(ctx context.Context, service *GtfsService, server models.ObaServer, trigger StaticRefreshTrigger, maxRetries int) bool {
	// Keep configuration validation atomic with campaign registration. This
	// short read lock does not wait on the static publication refresh lock.
	service.StaticStore.mu.RLock()
	defer service.StaticStore.mu.RUnlock()
	if ctx.Err() != nil || !service.StaticStore.isConfiguredLocked(server) {
		return false
	}
	key := server.ServerKey()
	fingerprint := staticConfigFingerprint(server)
	campaignCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.nextGeneration++
	generation := c.nextGeneration
	if active, ok := c.active[key]; ok {
		if active.fingerprint == fingerprint {
			c.mu.Unlock()
			cancel()
			return false
		}
		active.cancel()
	}
	if trigger == StaticRefreshConfigChange {
		for historyKey := range c.histories {
			if len(historyKey) >= len(key)+1 && historyKey[:len(key)+1] == key+"|" {
				delete(c.histories, historyKey)
			}
		}
	}
	c.active[key] = activeStaticCampaign{fingerprint: fingerprint, generation: generation, cancel: cancel}
	c.mu.Unlock()

	go func() {
		defer cancel()
		service.runStaticRefreshCampaign(campaignCtx, server, trigger, maxRetries, c.policy)
		c.mu.Lock()
		if active, ok := c.active[key]; ok && active.generation == generation {
			delete(c.active, key)
		}
		c.mu.Unlock()
	}()
	return true
}

// reconcile cancels obsolete campaigns and removes state belonging to removed or changed configurations.
func (c *staticRefreshCoordinator) reconcile(servers []models.ObaServer) {
	configured := make(map[string]string, len(servers))
	configuredFeeds := make(map[string]struct{})
	for _, server := range servers {
		configured[server.ServerKey()] = staticConfigFingerprint(server)
		if feedURL, ok := staticFeedURL(server); ok {
			configuredFeeds[staticFeedHistoryKey(server, feedURL)] = struct{}{}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, active := range c.active {
		if configured[key] != active.fingerprint {
			active.cancel()
			delete(c.active, key)
		}
	}
	for key, run := range c.backendRuns {
		if configured[key] != run.fingerprint {
			delete(c.backendRuns, key)
		}
	}
	for key := range c.backendCampaigns {
		if configured[key.server] != key.fingerprint {
			delete(c.backendCampaigns, key)
		}
	}
	for key, content := range c.snapshotContent {
		if configured[key] != content.fingerprint {
			delete(c.snapshotContent, key)
		}
	}
	for key, content := range c.published {
		if configured[key] != content.fingerprint {
			delete(c.published, key)
		}
	}
	for key := range c.histories {
		if _, ok := configuredFeeds[key]; !ok {
			delete(c.histories, key)
		}
	}
	for key, state := range c.mismatches {
		if configured[key] != state.fingerprint {
			delete(c.mismatches, key)
		}
	}
	for key, content := range c.superseded {
		if configured[key] != content.fingerprint {
			delete(c.superseded, key)
		}
	}
	for key, state := range c.relations {
		if configured[key] != state.fingerprint {
			delete(c.relations, key)
		}
	}
	for key, state := range c.feedChecks {
		if configured[key] != state.fingerprint {
			delete(c.feedChecks, key)
		}
	}
}

// unchanged reports whether hash and the server's static configuration
// match its last successful publish.
func (c *staticRefreshCoordinator) unchanged(server models.ObaServer, hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	content, ok := c.published[server.ServerKey()]
	return ok && hash != "" && content.fingerprint == staticConfigFingerprint(server) && content.hash == hash
}

// recordPublished records the zip hash of a successful publication unless its context was canceled.
func (c *staticRefreshCoordinator) recordPublished(ctx context.Context, server models.ObaServer, hash string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	key := server.ServerKey()
	content := publishedStaticContent{fingerprint: staticConfigFingerprint(server), hash: hash}
	if previous, ok := c.snapshotContent[key]; ok && previous.fingerprint == content.fingerprint && previous.hash != hash {
		// A newer bundle replaced the snapshot. A backend still reporting the
		// old one's hash is behind Watchdog, not ahead of it.
		c.superseded[key] = previous
		if _, hadRelation := c.relations[key]; hadRelation {
			c.relations[key] = backendRelationState{
				fingerprint: content.fingerprint,
				relation:    BackendBundleUnknown,
				debouncing:  true,
			}
		}
	} else if !ok || previous.fingerprint != content.fingerprint {
		delete(c.superseded, key)
		delete(c.relations, key)
	}
	c.published[key] = content
	// Retain the identity of the snapshot still in use if a later refresh
	// fails and invalidates only the unchanged-download optimization.
	c.snapshotContent[key] = content
	return true
}

// forgetPublished makes the server's next successful download publish even if
// its bytes match, because the published snapshot can no longer be trusted
// (its schedule was marked unavailable, or a publish failed).
func (c *staticRefreshCoordinator) forgetPublished(server models.ObaServer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.published, server.ServerKey())
}

func (c *staticRefreshCoordinator) isConservative(server models.ObaServer, feedURL string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	history := c.histories[staticFeedHistoryKey(server, feedURL)]
	return history != nil && history.conservative
}

func (c *staticRefreshCoordinator) history(server models.ObaServer, feedURL string) (staticFeedFailureHistory, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	history := c.histories[staticFeedHistoryKey(server, feedURL)]
	if history == nil {
		return staticFeedFailureHistory{}, false
	}
	return *history, true
}

func (c *staticRefreshCoordinator) recordSuccess(server models.ObaServer, feedURL string) staticFeedFailureHistory {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordSuccessLocked(server, feedURL)
}

func (c *staticRefreshCoordinator) recordCampaignSuccess(ctx context.Context, server models.ObaServer, feedURL string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	c.recordSuccessLocked(server, feedURL)
	return true
}

func (c *staticRefreshCoordinator) recordSuccessLocked(server models.ObaServer, feedURL string) staticFeedFailureHistory {
	key := staticFeedHistoryKey(server, feedURL)
	delete(c.histories, key)
	return staticFeedFailureHistory{}
}

func (c *staticRefreshCoordinator) recordFailure(server models.ObaServer, feedURL string, failure *StaticFeedError, at time.Time) staticFeedFailureHistory {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordFailureLocked(server, feedURL, failure, at)
}

func (c *staticRefreshCoordinator) recordCampaignFailure(ctx context.Context, server models.ObaServer, feedURL string, failure *StaticFeedError, at time.Time) (staticFeedFailureHistory, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return staticFeedFailureHistory{}, false
	}
	return c.recordFailureLocked(server, feedURL, failure, at), true
}

func (c *staticRefreshCoordinator) recordFailureLocked(server models.ObaServer, feedURL string, failure *StaticFeedError, at time.Time) staticFeedFailureHistory {
	key := staticFeedHistoryKey(server, feedURL)
	history := c.histories[key]
	if history == nil {
		history = &staticFeedFailureHistory{failureSince: at}
		c.histories[key] = history
	}
	if failurePolicyClass(history.lastReason) != failurePolicyClass(failure.Reason) {
		history.failedEscalatingCampaigns = 0
		history.identicalFailures = 0
		history.lastContentHash = ""
	}
	history.lastStage = failure.Stage
	history.lastReason = failure.Reason
	if failure.ContentHash == "" || failure.ContentHash != history.lastContentHash {
		history.lastContentHash = failure.ContentHash
		history.identicalFailures = 1
	} else {
		history.identicalFailures++
	}
	switch failure.Reason {
	case StaticReasonInvalidURL, StaticReasonUnauthorized, StaticReasonForbidden:
		history.conservative = true
	case StaticReasonDuplicateID, StaticReasonInvalidZIP, StaticReasonMissingAgencyFile, StaticReasonEmptyAgencyFile, StaticReasonMissingAgencyID, StaticReasonAmbiguousAgency, StaticReasonUnknownAgency, StaticReasonInvalidSchedule:
		if failure.ContentHash != "" && history.identicalFailures >= 3 {
			history.conservative = true
		}
	}
	return *history
}

func failurePolicyClass(reason StaticFailureReason) string {
	switch reason {
	case StaticReasonTimeout, StaticReasonConnection, StaticReasonServerError, StaticReasonUnknown:
		return "transient"
	case StaticReasonNotFound, StaticReasonGone:
		return "missing"
	case StaticReasonDuplicateID, StaticReasonInvalidZIP, StaticReasonMissingAgencyFile, StaticReasonEmptyAgencyFile, StaticReasonMissingAgencyID, StaticReasonAmbiguousAgency, StaticReasonUnknownAgency, StaticReasonInvalidSchedule:
		return "content"
	default:
		return string(reason)
	}
}

// recordFailedCampaign counts a failed campaign toward the conservative
// classification when its trigger escalates.
func (c *staticRefreshCoordinator) recordFailedCampaign(server models.ObaServer, feedURL string, trigger StaticRefreshTrigger) {
	if !trigger.policy().escalates {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordFailedCampaignLocked(server, feedURL)
}

func (c *staticRefreshCoordinator) recordCampaignExhaustion(ctx context.Context, server models.ObaServer, feedURL string, trigger StaticRefreshTrigger) bool {
	if !trigger.policy().escalates {
		return ctx.Err() == nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	c.recordFailedCampaignLocked(server, feedURL)
	return true
}

func (c *staticRefreshCoordinator) recordFailedCampaignLocked(server models.ObaServer, feedURL string) {
	history := c.histories[staticFeedHistoryKey(server, feedURL)]
	if history == nil {
		return
	}
	history.failedEscalatingCampaigns++
	switch history.lastReason {
	case StaticReasonNotFound, StaticReasonGone:
		history.conservative = true
	default:
		if history.failedEscalatingCampaigns >= conservativeAfterFailedCampaigns {
			history.conservative = true
		}
	}
}

// runStaticRefreshCampaign downloads the server's upstream-merged static feed,
// retrying with the policy's backoff until it publishes, the budget runs out,
// or the feed is classified as conservative.
func (gs *GtfsService) runStaticRefreshCampaign(ctx context.Context, server models.ObaServer, trigger StaticRefreshTrigger, maxRetries int, policy staticRefreshRetryPolicy) {
	feedURL, ok := staticFeedURL(server)
	if !ok {
		gs.Logger.Error("Expected one upstream-merged static feed; merge feeds upstream", "server_name", server.ServerName)
		gs.StaticStore.WithRefreshLock(func() {
			if gs.StaticStore.IsConfigured(server) {
				gs.StaticStore.ScheduleStore().MarkUnavailable(server)
			}
		})
		gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: time.Now().UTC(), GaveUp: true})
		return
	}
	cache, err := newStaticArtifactCache(maxStaticCampaignCacheSize)
	if err != nil {
		gs.Logger.Error("Failed to create GTFS static campaign cache", "server_name", server.ServerName, "error", err)
		return
	}
	defer cache.Close()

	// A trigger that probes conservative feeds gives one already classified
	// as conservative a single attempt: a failure ends the campaign.
	conservativeProbe := trigger.policy().probeOnceWhenConservative && gs.refreshCoordinator.isConservative(server, feedURL)
	pending := true

	started := time.Now()
	deadline := started.Add(policy.budget)
	delay := time.Duration(0)
	var lastFailure *StaticFeedError
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		attemptedAt := time.Now().UTC()
		gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: attemptedAt, Retrying: true})
		maxRetryAfter := time.Duration(0)
		if pending {
			data, downloadErr := fetchGTFSBundleData(ctx, gs.Client, feedURL, server.AgencyID, maxRetries)
			if downloadErr == nil {
				admitted := gs.withStaticParseSlot(ctx, func() {
					_, downloadErr = parseDownloadedGTFSBundle(gs.parseStatic, data, feedURL, server.AgencyID)
				})
				if !admitted {
					return
				}
			}
			if ctx.Err() != nil {
				return
			}
			if downloadErr == nil {
				artifact, cacheErr := cache.Put(feedURL, data)
				if cacheErr != nil {
					downloadErr = &StaticFeedError{Stage: StaticFailureDownload, Reason: StaticReasonResponseTooLarge, Err: cacheErr}
				} else {
					pending = false
					lastFailure = nil
					history, _ := gs.refreshCoordinator.history(server, feedURL)
					gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, Success: true, Conservative: history.conservative, ContentHash: artifact.hash})
				}
			}
			if downloadErr != nil {
				failure := asStaticFeedError(downloadErr)
				lastFailure = failure
				history, recorded := gs.refreshCoordinator.recordCampaignFailure(ctx, server, feedURL, failure, attemptedAt)
				if !recorded {
					return
				}
				gs.refreshCoordinator.recordFeedCheck(server, attemptedAt, failure)
				gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, FailureSince: history.failureSince, Stage: failure.Stage, Reason: failure.Reason, Conservative: history.conservative})
				maxRetryAfter = failure.RetryAfter
			}
		}

		if conservativeProbe {
			if pending {
				gs.finishFailedStaticCampaign(ctx, server, trigger, attemptedAt, feedURL, lastFailure)
				return
			}
			conservativeProbe = false
		}

		if !pending {
			artifact, _ := cache.Get(feedURL)
			hash := artifact.hash
			reportSuccess := func() {
				if !gs.refreshCoordinator.recordCampaignSuccess(ctx, server, feedURL) {
					return
				}
				gs.refreshCoordinator.recordFeedCheck(server, attemptedAt, nil)
				gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, Success: true, ContentHash: hash})
				gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: attemptedAt, Success: true})
			}
			// A byte-identical feed under an unchanged configuration would
			// parse to the snapshot already published, so confirm it as
			// current instead of paying for a full parse and store swap.
			if gs.refreshCoordinator.unchanged(server, hash) && gs.confirmUnchangedStaticSnapshot(ctx, server) {
				reportSuccess()
				return
			}
			var err error
			var publication *StaticVersionPublication
			if !gs.withStaticParseSlot(ctx, func() { publication, err = gs.publishStaticCampaign(ctx, server, feedURL, cache) }) {
				return
			}
			if err == nil {
				if gs.refreshCoordinator.recordPublished(ctx, server, hash) {
					// Recording waits on the investigation database for up
					// to its write timeout, so it runs once the parse slot
					// is free for other servers.
					gs.reportStaticVersion(publication)
					reportSuccess()
				}
				return
			}
			gs.refreshCoordinator.forgetPublished(server)
			if ctx.Err() != nil || errors.Is(err, errStaticServerUnconfigured) {
				return
			}
			failure := *asStaticFeedError(err)
			if failure.ContentHash == "" {
				failure.ContentHash = hash
			}
			history, recorded := gs.refreshCoordinator.recordCampaignFailure(ctx, server, feedURL, &failure, attemptedAt)
			if !recorded {
				return
			}
			gs.refreshCoordinator.recordFeedCheck(server, attemptedAt, &failure)
			gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, FailureSince: history.failureSince, Stage: failure.Stage, Reason: failure.Reason, Conservative: history.conservative})
			lastFailure = &failure
			cache.Delete(feedURL)
			pending = true
		}

		if delay == 0 {
			delay = policy.initialDelay
		} else {
			delay = min(delay*2, policy.maxDelay)
		}
		if maxRetryAfter > delay {
			delay = maxRetryAfter
		}
		if time.Now().Add(delay).After(deadline) || (pending && gs.refreshCoordinator.isConservative(server, feedURL)) {
			gs.finishFailedStaticCampaign(ctx, server, trigger, attemptedAt, feedURL, lastFailure)
			return
		}
	}
}

// withStaticParseSlot runs fn once no other campaign is parsing or
// publishing, and reports false without running it if ctx ends first. Only the
// memory-heavy work takes the slot: a download can spend many minutes in
// retries, and holding the slot through that would stall every other server.
func (gs *GtfsService) withStaticParseSlot(ctx context.Context, fn func()) bool {
	select {
	case <-ctx.Done():
		return false
	case gs.staticParseSlot <- struct{}{}:
	}
	defer func() { <-gs.staticParseSlot }()
	fn()
	return true
}

func (gs *GtfsService) finishFailedStaticCampaign(ctx context.Context, server models.ObaServer, trigger StaticRefreshTrigger, attemptedAt time.Time, feedURL string, failure *StaticFeedError) {
	if !gs.refreshCoordinator.recordCampaignExhaustion(ctx, server, feedURL, trigger) {
		return
	}
	if history, ok := gs.refreshCoordinator.history(server, feedURL); ok {
		gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{
			AttemptedAt: attemptedAt, FailureSince: history.failureSince,
			Stage: history.lastStage, Reason: history.lastReason, Conservative: history.conservative,
		})
	}
	gs.refreshCoordinator.forgetPublished(server)
	gs.StaticStore.WithRefreshLock(func() {
		if gs.StaticStore.IsConfigured(server) {
			gs.StaticStore.ScheduleStore().MarkUnavailable(server)
		}
	})
	gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: attemptedAt, GaveUp: true})
	gs.Logger.Error("GTFS static refresh campaign failed; preserving prior complete snapshot", "server_name", server.ServerName, "agency_id", server.AgencyID, "feed_url", utils.SanitizeServerURL(feedURL), "failure", failure)
}

// confirmUnchangedStaticSnapshot renews the fetch time of the snapshot the
// server already holds. It reports false when there is nothing to confirm (the
// entry is unconfigured or holds no snapshot), so the caller publishes.
func (gs *GtfsService) confirmUnchangedStaticSnapshot(ctx context.Context, server models.ObaServer) bool {
	confirmed := false
	gs.StaticStore.WithRefreshLock(func() {
		if ctx.Err() != nil || !gs.StaticStore.IsConfigured(server) {
			return
		}
		confirmed = gs.StaticStore.TouchServerSnapshot(server, time.Now().UTC())
	})
	return confirmed
}

func (gs *GtfsService) publishStaticCampaign(ctx context.Context, server models.ObaServer, feedURL string, cache *staticArtifactCache) (*StaticVersionPublication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := cache.Read(feedURL)
	if err != nil {
		return nil, sanitizeStaticFeedError(&StaticFeedError{
			FeedURL: feedURL,
			Stage:   StaticFailureCompleteValidate,
			Reason:  StaticReasonUnknown,
			Err:     err,
		}, feedURL)
	}
	bundle, err := gs.parseStatic(data, feedURL, server.AgencyID)
	if err != nil {
		return nil, err
	}
	if reason := validateStaticAgencyDiscovery(server, bundle); reason != "" {
		artifact, _ := cache.Get(feedURL)
		return nil, &StaticFeedError{FeedURL: feedURL, Stage: StaticFailureAgencyDiscovery, Reason: reason, ContentHash: artifact.hash, Err: fmt.Errorf("static feed %s failed agency discovery: %s", utils.SanitizeServerURL(feedURL), reason)}
	}
	raw, err := parseRawScheduleFeed(feedURL, data, server)
	if err != nil {
		artifact, _ := cache.Get(feedURL)
		return nil, &StaticFeedError{FeedURL: feedURL, Stage: StaticFailureSchedule, Reason: StaticReasonInvalidSchedule, ContentHash: artifact.hash, Err: fmt.Errorf("validate schedule in %s: %w", utils.SanitizeServerURL(feedURL), err)}
	}
	schedules, err := compileRawSchedule(server, raw)
	if err != nil {
		return nil, &StaticFeedError{Stage: StaticFailureCompleteValidate, Reason: StaticReasonInvalidSchedule, Err: err}
	}
	downloaded := downloadedStaticFeed{url: feedURL, data: data, bundle: bundle}

	configured := false
	var storeErr error
	var published publishedStatic
	gs.StaticStore.WithRefreshLock(func() {
		if ctx.Err() != nil || !gs.StaticStore.IsConfigured(server) {
			return
		}
		configured = true
		published, storeErr = storeStaticSnapshot(server, bundle, gs.StaticStore, gs.BoundingBoxStore, gs.RouteAgencyIndex, gs.Observer, gs.Logger)
		if storeErr != nil {
			gs.StaticStore.ScheduleStore().MarkUnavailable(server)
			return
		}
		gs.StaticStore.ScheduleStore().Replace(server, schedules)
		if gs.MappingObserver != nil {
			func() {
				defer func() { _ = recover() }()
				gs.MappingObserver(server, classifyStaticFeedMappings(server, downloaded))
			}()
		}
	})
	if !configured {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errStaticServerUnconfigured
	}
	if storeErr != nil {
		artifact, _ := cache.Get(feedURL)
		failure := staticPublicationFailure(storeErr, feedURL, artifact.hash)
		report.ReportErrorWithSentryOptions(failure, report.SentryReportOptions{Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName}, Level: sentry.LevelError})
		return nil, failure
	}
	artifact, _ := cache.Get(feedURL)
	return published.publication(server, []StaticFeedContent{{URL: feedURL, ContentSHA256: artifact.hash}}), nil
}

func validateStaticAgencyDiscovery(server models.ObaServer, bundle *remoteGtfs.Static) StaticFailureReason {
	if bundle == nil || len(bundle.Agencies) == 0 {
		return StaticReasonEmptyAgencyFile
	}
	blank := 0
	matched := false
	for _, agency := range bundle.Agencies {
		if agency.Id == "" {
			blank++
		}
		if !server.IsServerScoped() && agency.Id == server.AgencyID {
			matched = true
		}
	}
	if server.IsServerScoped() {
		if blank == len(bundle.Agencies) {
			return StaticReasonMissingAgencyID
		}
		if blank > 0 {
			return StaticReasonAmbiguousAgency
		}
		return ""
	}
	if len(bundle.Agencies) == 1 && blank == 1 {
		return ""
	}
	if blank > 0 {
		return StaticReasonAmbiguousAgency
	}
	if !matched {
		return StaticReasonUnknownAgency
	}
	return ""
}

// Static refresh errors are reported at their origin. This helper exists so a
// caller can retain the previous snapshot without duplicating error reporting.
func reportStaticCampaignError(err error, server models.ObaServer) {
	report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
		Tags:  map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		Level: sentry.LevelError,
	})
}

// DiffAgencySets compares a backend's static agency coverage (agencyIDs from
// /api/where/metrics.json) with the agencies in Watchdog's published static
// snapshot. Both inputs are treated as sets, so ordering and duplicate IDs do
// not affect the result. Both returned slices are sorted.
func DiffAgencySets(backend map[string]bool, local []string) (backendOnly, localOnly []string) {
	localSet := make(map[string]struct{}, len(local))
	for _, id := range local {
		localSet[id] = struct{}{}
	}
	for id, reported := range backend {
		if !reported {
			continue
		}
		if _, ok := localSet[id]; !ok {
			backendOnly = append(backendOnly, id)
		}
	}
	for id := range localSet {
		if !backend[id] {
			localOnly = append(localOnly, id)
		}
	}
	sort.Strings(backendOnly)
	sort.Strings(localOnly)
	return backendOnly, localOnly
}

// agencyMismatchSignature returns a stable identity for a mismatch, or "" when
// the sets agree. Inputs must be sorted (DiffAgencySets guarantees it). NUL
// separates IDs and \x01 separates the two directions, so no agency ID can make
// two different mismatches collide.
func agencyMismatchSignature(backendOnly, localOnly []string) string {
	if len(backendOnly) == 0 && len(localOnly) == 0 {
		return ""
	}
	return "+" + strings.Join(backendOnly, "\x00") + "\x01-" + strings.Join(localOnly, "\x00")
}

// observeAgencyMismatch records sig as the server's current mismatch and
// reports whether it is new. An empty sig means the sets converged and clears
// the state, so a later recurrence triggers again.
func (c *staticRefreshCoordinator) observeAgencyMismatch(server models.ObaServer, sig string) bool {
	key := server.ServerKey()
	fingerprint := staticConfigFingerprint(server)

	c.mu.Lock()
	defer c.mu.Unlock()
	if sig == "" {
		delete(c.mismatches, key)
		return false
	}
	if prev, ok := c.mismatches[key]; ok && prev.fingerprint == fingerprint && prev.signature == sig {
		return false
	}
	c.mismatches[key] = agencyMismatchState{fingerprint: fingerprint, signature: sig}
	return true
}

// forgetAgencyMismatch drops the recorded mismatch so the next observation can
// trigger again. Used when a refresh could not be started.
func (c *staticRefreshCoordinator) forgetAgencyMismatch(server models.ObaServer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.mismatches, server.ServerKey())
}
