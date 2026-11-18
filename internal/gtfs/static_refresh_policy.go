package gtfs

import "time"

// StaticRefreshTrigger names what started a static refresh campaign.
type StaticRefreshTrigger string

const (
	// StaticRefreshStartup downloads every configured feed once at boot.
	StaticRefreshStartup StaticRefreshTrigger = "startup"
	// StaticRefreshBackendChanged follows a backend metadata hash that differs
	// from the local snapshot. This is the normal way a bundle is refreshed.
	StaticRefreshBackendChanged StaticRefreshTrigger = "backend_changed"
	// StaticRefreshFallback downloads the feed while the backend metadata hash
	// has been unusable for too long. See StaticFallbackEvery.
	StaticRefreshFallback StaticRefreshTrigger = "fallback"
	// StaticRefreshConfigChange follows a change to the entry's static configuration.
	StaticRefreshConfigChange StaticRefreshTrigger = "config_change"
	// StaticRefreshAgencyMismatch follows a backend whose static agency
	// coverage differs from Watchdog's snapshot.
	StaticRefreshAgencyMismatch StaticRefreshTrigger = "agency_mismatch"
)

// While the backend metadata hash is unusable, a fallback download runs once
// the hash has not worked for StaticFallbackEvery, and again every
// StaticFallbackEvery while it stays unusable.
const StaticFallbackEvery = 24 * time.Hour

// staticTriggerPolicy is what a trigger permits when its campaign fails.
//
// The rules below apply only to recurring triggers. A one-off trigger
// (startup, config change, a single backend mismatch) says nothing about how
// a feed behaves over time, so it never marks a feed conservative on its own.
type staticTriggerPolicy struct {
	// escalates makes a failed campaign count toward classifying the feed as
	// conservative: a NotFound/Gone failure classifies it at once, and
	// conservativeAfterFailedCampaigns failures of any other kind classify it.
	escalates bool
	// probeOnceWhenConservative limits a campaign for a conservative feed to a
	// single attempt instead of the full retry loop.
	probeOnceWhenConservative bool
}

// staticTriggerPolicies is the single place that decides which triggers
// escalate. A trigger missing from this table escalates nothing.
var staticTriggerPolicies = map[StaticRefreshTrigger]staticTriggerPolicy{
	StaticRefreshFallback: {escalates: true, probeOnceWhenConservative: true},
}

func (t StaticRefreshTrigger) policy() staticTriggerPolicy {
	return staticTriggerPolicies[t]
}

// conservativeAfterFailedCampaigns is how many failed escalating campaigns
// mark a feed conservative when no single failure already did.
const conservativeAfterFailedCampaigns = 7

// staticRefreshRetryPolicy is the retry loop inside one campaign: the delay
// between attempts doubles from initialDelay up to maxDelay, and the campaign
// gives up once the next attempt would start after budget has elapsed.
type staticRefreshRetryPolicy struct {
	initialDelay time.Duration
	maxDelay     time.Duration
	budget       time.Duration
}

var defaultStaticRefreshRetryPolicy = staticRefreshRetryPolicy{
	initialDelay: 5 * time.Minute,
	maxDelay:     2 * time.Hour,
	budget:       12 * time.Hour,
}
