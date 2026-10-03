package gtfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
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

type StaticRefreshTrigger string

const (
	StaticRefreshStartup      StaticRefreshTrigger = "startup"
	StaticRefreshDaily        StaticRefreshTrigger = "daily"
	StaticRefreshConfigChange StaticRefreshTrigger = "config_change"
)

const maxStaticCampaignCacheSize = int64(2 << 30)
const maxStaticFeedDownloadSize = int64(512 << 20)

type staticFeedFailureHistory struct {
	failureSince      time.Time
	failedDailyRuns   int
	identicalFailures int
	lastContentHash   string
	conservative      bool
	lastStage         StaticFailureStage
	lastReason        StaticFailureReason
}

type activeStaticCampaign struct {
	fingerprint string
	generation  uint64
	cancel      context.CancelFunc
}

type staticRefreshCoordinator struct {
	mu             sync.Mutex
	active         map[string]activeStaticCampaign
	histories      map[string]*staticFeedFailureHistory
	policy         staticRefreshRetryPolicy
	nextGeneration uint64
}

func newStaticRefreshCoordinator() *staticRefreshCoordinator {
	return &staticRefreshCoordinator{
		active:    make(map[string]activeStaticCampaign),
		histories: make(map[string]*staticFeedFailureHistory),
		policy:    dailyStaticRefreshRetryPolicy,
	}
}

func staticFeedHistoryKey(server models.ObaServer, feedURL string) string {
	return server.ServerKey() + "|" + feedURL
}

func (c *staticRefreshCoordinator) start(ctx context.Context, service *GtfsService, server models.ObaServer, trigger StaticRefreshTrigger, maxRetries int) {
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
			return
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
}

func (c *staticRefreshCoordinator) reconcile(servers []models.ObaServer) {
	configured := make(map[string]string, len(servers))
	configuredFeeds := make(map[string]struct{})
	for _, server := range servers {
		configured[server.ServerKey()] = staticConfigFingerprint(server)
		for _, feedURL := range server.GtfsStaticFeeds {
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
	for key := range c.histories {
		if _, ok := configuredFeeds[key]; !ok {
			delete(c.histories, key)
		}
	}
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
		history.failedDailyRuns = 0
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
	case StaticReasonInvalidZIP, StaticReasonMissingAgencyFile, StaticReasonEmptyAgencyFile, StaticReasonMissingAgencyID, StaticReasonAmbiguousAgency, StaticReasonUnknownAgency, StaticReasonInvalidSchedule:
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
	case StaticReasonInvalidZIP, StaticReasonMissingAgencyFile, StaticReasonEmptyAgencyFile, StaticReasonMissingAgencyID, StaticReasonAmbiguousAgency, StaticReasonUnknownAgency, StaticReasonInvalidSchedule:
		return "content"
	default:
		return string(reason)
	}
}

func (c *staticRefreshCoordinator) recordFailedCampaign(server models.ObaServer, feedURLs []string, trigger StaticRefreshTrigger) {
	if trigger != StaticRefreshDaily {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordFailedCampaignLocked(server, feedURLs)
}

func (c *staticRefreshCoordinator) recordCampaignExhaustion(ctx context.Context, server models.ObaServer, feedURLs []string, trigger StaticRefreshTrigger) bool {
	if trigger != StaticRefreshDaily {
		return ctx.Err() == nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	c.recordFailedCampaignLocked(server, feedURLs)
	return true
}

func (c *staticRefreshCoordinator) recordFailedCampaignLocked(server models.ObaServer, feedURLs []string) {
	for _, feedURL := range feedURLs {
		history := c.histories[staticFeedHistoryKey(server, feedURL)]
		if history == nil {
			continue
		}
		history.failedDailyRuns++
		switch history.lastReason {
		case StaticReasonNotFound, StaticReasonGone:
			history.conservative = true
		default:
			if history.failedDailyRuns >= 7 {
				history.conservative = true
			}
		}
	}
}

func (gs *GtfsService) runStaticRefreshCampaign(ctx context.Context, server models.ObaServer, trigger StaticRefreshTrigger, maxRetries int, policy staticRefreshRetryPolicy) {
	cache, err := newStaticArtifactCache(maxStaticCampaignCacheSize)
	if err != nil {
		gs.Logger.Error("Failed to create GTFS static campaign cache", "server_name", server.ServerName, "error", err)
		return
	}
	defer cache.Close()

	pending := make(map[string]struct{}, len(server.GtfsStaticFeeds))
	conservativeProbe := trigger == StaticRefreshDaily
	for _, feedURL := range server.GtfsStaticFeeds {
		if !conservativeProbe || gs.refreshCoordinator.isConservative(server, feedURL) {
			pending[feedURL] = struct{}{}
		}
	}
	if conservativeProbe && len(pending) == 0 {
		for _, feedURL := range server.GtfsStaticFeeds {
			pending[feedURL] = struct{}{}
		}
		conservativeProbe = false
	}

	started := time.Now()
	deadline := started.Add(policy.budget)
	delay := time.Duration(0)
	lastFailures := make(map[string]*StaticFeedError)
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
		feedURLs := sortedPendingFeeds(pending)
		maxRetryAfter := time.Duration(0)
		for _, feedURL := range feedURLs {
			_, data, downloadErr := downloadGTFSBundleData(ctx, gs.Client, feedURL, server.AgencyID, maxRetries)
			if ctx.Err() != nil {
				return
			}
			if downloadErr == nil {
				artifact, cacheErr := cache.Put(feedURL, data)
				if cacheErr != nil {
					downloadErr = &StaticFeedError{Stage: StaticFailureDownload, Reason: StaticReasonResponseTooLarge, Err: cacheErr}
				} else {
					delete(pending, feedURL)
					delete(lastFailures, feedURL)
					history, _ := gs.refreshCoordinator.history(server, feedURL)
					gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, Success: true, Conservative: history.conservative, ContentHash: artifact.hash})
					continue
				}
			}
			failure := asStaticFeedError(downloadErr)
			lastFailures[feedURL] = failure
			history, recorded := gs.refreshCoordinator.recordCampaignFailure(ctx, server, feedURL, failure, attemptedAt)
			if !recorded {
				return
			}
			gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, FailureSince: history.failureSince, Stage: failure.Stage, Reason: failure.Reason, Conservative: history.conservative})
			if failure.RetryAfter > maxRetryAfter {
				maxRetryAfter = failure.RetryAfter
			}
		}

		if conservativeProbe {
			if len(pending) > 0 {
				gs.finishFailedStaticCampaign(ctx, server, trigger, attemptedAt, pending, lastFailures)
				return
			}
			for _, feedURL := range server.GtfsStaticFeeds {
				if _, cached := cache.Get(feedURL); !cached {
					pending[feedURL] = struct{}{}
				}
			}
			conservativeProbe = false
			if len(pending) > 0 {
				continue
			}
		}

		if len(pending) == 0 {
			if err := gs.publishStaticCampaign(ctx, server, cache); err == nil {
				for _, feedURL := range server.GtfsStaticFeeds {
					if !gs.refreshCoordinator.recordCampaignSuccess(ctx, server, feedURL) {
						return
					}
					artifact, _ := cache.Get(feedURL)
					gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, Success: true, ContentHash: artifact.hash})
				}
				gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: attemptedAt, Success: true})
				return
			} else {
				if ctx.Err() != nil || errors.Is(err, errStaticServerUnconfigured) {
					return
				}
				failure := asStaticFeedError(err)
				failedFeeds := server.GtfsStaticFeeds
				if failure.FeedURL != "" {
					failedFeeds = []string{failure.FeedURL}
				}
				for _, feedURL := range failedFeeds {
					artifact, _ := cache.Get(feedURL)
					feedFailure := *failure
					if feedFailure.ContentHash == "" {
						feedFailure.ContentHash = artifact.hash
					}
					history, recorded := gs.refreshCoordinator.recordCampaignFailure(ctx, server, feedURL, &feedFailure, attemptedAt)
					if !recorded {
						return
					}
					gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{AttemptedAt: attemptedAt, FailureSince: history.failureSince, Stage: feedFailure.Stage, Reason: feedFailure.Reason, Conservative: history.conservative})
					lastFailures[feedURL] = &feedFailure
					cache.Delete(feedURL)
					pending[feedURL] = struct{}{}
				}
			}
		}

		if delay == 0 {
			delay = policy.initialDelay
		} else {
			delay = min(delay*2, policy.maxDelay)
		}
		if maxRetryAfter > delay {
			delay = maxRetryAfter
		}
		if time.Now().Add(delay).After(deadline) || containsConservativeFailure(server, pending, gs.refreshCoordinator) {
			gs.finishFailedStaticCampaign(ctx, server, trigger, attemptedAt, pending, lastFailures)
			return
		}
	}
}

func sortedPendingFeeds(pending map[string]struct{}) []string {
	result := make([]string, 0, len(pending))
	for feedURL := range pending {
		result = append(result, feedURL)
	}
	sort.Strings(result)
	return result
}

func containsConservativeFailure(server models.ObaServer, pending map[string]struct{}, coordinator *staticRefreshCoordinator) bool {
	for feedURL := range pending {
		if coordinator.isConservative(server, feedURL) {
			return true
		}
	}
	return false
}

func (gs *GtfsService) finishFailedStaticCampaign(ctx context.Context, server models.ObaServer, trigger StaticRefreshTrigger, attemptedAt time.Time, pending map[string]struct{}, failures map[string]*StaticFeedError) {
	failedFeeds := sortedPendingFeeds(pending)
	if !gs.refreshCoordinator.recordCampaignExhaustion(ctx, server, failedFeeds, trigger) {
		return
	}
	for _, feedURL := range failedFeeds {
		history, ok := gs.refreshCoordinator.history(server, feedURL)
		if !ok {
			continue
		}
		gs.reportStaticFeedRefresh(server, feedURL, StaticFeedRefreshObservation{
			AttemptedAt: attemptedAt, FailureSince: history.failureSince,
			Stage: history.lastStage, Reason: history.lastReason, Conservative: history.conservative,
		})
	}
	gs.StaticStore.WithRefreshLock(func() {
		if gs.StaticStore.IsConfigured(server) {
			gs.StaticStore.ScheduleStore().MarkUnavailable(server)
		}
	})
	gs.reportStaticRefresh(server, StaticRefreshObservation{AttemptedAt: attemptedAt, GaveUp: true})
	gs.Logger.Error("GTFS static refresh campaign failed; preserving prior complete snapshot", "server_name", server.ServerName, "agency_id", server.AgencyID, "failed_feeds", failedFeeds, "failures", failures)
}

func (gs *GtfsService) publishStaticCampaign(ctx context.Context, server models.ObaServer, cache *staticArtifactCache) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bundles := make([]*remoteGtfs.Static, 0, len(server.GtfsStaticFeeds))
	downloaded := make([]downloadedStaticFeed, 0, len(server.GtfsStaticFeeds))
	raws := make([]rawScheduleFeed, 0, len(server.GtfsStaticFeeds))
	for _, feedURL := range server.GtfsStaticFeeds {
		data, err := cache.Read(feedURL)
		if err != nil {
			return &StaticFeedError{Stage: StaticFailureCompleteValidate, Reason: StaticReasonUnknown, Err: err}
		}
		bundle, err := parseStaticBundleData(data, feedURL, server.AgencyID)
		if err != nil {
			return err
		}
		if reason := validateStaticAgencyDiscovery(server, bundle); reason != "" {
			artifact, _ := cache.Get(feedURL)
			return &StaticFeedError{FeedURL: feedURL, Stage: StaticFailureAgencyDiscovery, Reason: reason, ContentHash: artifact.hash, Err: fmt.Errorf("static feed %s failed agency discovery: %s", feedURL, reason)}
		}
		raw, err := parseRawScheduleFeed(feedURL, data, server)
		if err != nil {
			artifact, _ := cache.Get(feedURL)
			return &StaticFeedError{FeedURL: feedURL, Stage: StaticFailureSchedule, Reason: StaticReasonInvalidSchedule, ContentHash: artifact.hash, Err: fmt.Errorf("validate schedule in %s: %w", feedURL, err)}
		}
		bundles = append(bundles, bundle)
		downloaded = append(downloaded, downloadedStaticFeed{url: feedURL, data: data, bundle: bundle})
		raws = append(raws, raw)
	}
	schedules, err := compileRawSchedules(server, raws)
	if err != nil {
		return &StaticFeedError{Stage: StaticFailureCompleteValidate, Reason: StaticReasonInvalidSchedule, Err: err}
	}

	configured := false
	var storeErr error
	gs.StaticStore.WithRefreshLock(func() {
		if ctx.Err() != nil || !gs.StaticStore.IsConfigured(server) {
			return
		}
		configured = true
		storeErr = storeStaticForServer(server, bundles, gs.StaticStore, gs.BoundingBoxStore, gs.RouteAgencyIndex, gs.Observer, gs.Logger)
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
			return err
		}
		return errStaticServerUnconfigured
	}
	if storeErr != nil {
		report.ReportErrorWithSentryOptions(storeErr, report.SentryReportOptions{Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName}, Level: sentry.LevelError})
		return &StaticFeedError{Stage: StaticFailurePublish, Reason: StaticReasonUnknown, Err: storeErr}
	}
	return nil
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

func startDailyStaticRefreshes(ctx context.Context, service *GtfsService, servers func() []models.ObaServer, logger *slog.Logger, interval time.Duration, maxRetries int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			configured := servers()
			logger.Info("Starting daily GTFS static refresh campaigns", "server_count", len(configured))
			service.StartStaticRefreshCampaigns(ctx, configured, StaticRefreshDaily, maxRetries)
		}
	}
}

// Static refresh errors are reported at their origin. This helper exists so a
// caller can retain the previous snapshot without duplicating error reporting.
func reportStaticCampaignError(err error, server models.ObaServer) {
	report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
		Tags:  map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		Level: sentry.LevelError,
	})
}
