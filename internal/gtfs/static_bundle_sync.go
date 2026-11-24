package gtfs

import (
	"time"

	"watchdog.onebusaway.org/internal/models"
)

// This file tracks two facts the app needs to explain an expired or
// mismatched static bundle:
//
//   - the result of the last attempt to fetch the configured feed URL, so an
//     expired bundle can be blamed on the right side (the agency's feed, or
//     the backend) only after the feed was actually checked;
//   - how the backend's bundle relates to Watchdog's, including the case
//     where Watchdog has already moved to a newer bundle and the backend still
//     serves the one Watchdog replaced.

// StaticFeedCheck is the result of the last completed attempt to fetch,
// validate and publish (or confirm unchanged) an entry's static feed.
type StaticFeedCheck struct {
	At      time.Time
	Success bool
	// Reason says why the attempt failed. It is empty on success.
	Reason StaticFailureReason
}

// BackendBundleRelation is how the backend's static bundle, as identified by
// its metadata hash, relates to Watchdog's snapshot.
type BackendBundleRelation int

const (
	// BackendBundleUnknown: no usable hash comparison yet, Watchdog's snapshot
	// changed since the last one, or the replaced bundle's hash has not yet
	// held long enough to call the backend behind.
	BackendBundleUnknown BackendBundleRelation = iota
	// BackendBundleMatch: the backend serves the bundle Watchdog holds.
	BackendBundleMatch
	// BackendBundleDiffers: the backend serves a bundle Watchdog does not
	// hold. A backend_changed campaign follows it.
	BackendBundleDiffers
	// BackendBundleBehind: the backend has kept serving the bundle Watchdog's
	// current snapshot replaced across a stable run of reads. Watchdog is
	// ahead, so downloading the feed again would only fetch the bundle it
	// already holds.
	BackendBundleBehind
)

func (r BackendBundleRelation) String() string {
	switch r {
	case BackendBundleMatch:
		return "match"
	case BackendBundleDiffers:
		return "differs"
	case BackendBundleBehind:
		return "behind"
	default:
		return "unknown"
	}
}

type feedCheckState struct {
	fingerprint string
	check       StaticFeedCheck
}

type backendRelationState struct {
	fingerprint string
	relation    BackendBundleRelation
	debouncing  bool
}

// recordFeedCheck records the outcome of one completed feed attempt unless the
// campaign was canceled. failure is nil on success.
func (c *staticRefreshCoordinator) recordFeedCheck(server models.ObaServer, at time.Time, failure *StaticFeedError) {
	check := StaticFeedCheck{At: at, Success: failure == nil}
	if failure != nil {
		check.Reason = failure.Reason
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.feedChecks[server.ServerKey()] = feedCheckState{fingerprint: staticConfigFingerprint(server), check: check}
}

// supersedesLocked reports whether hash is the snapshot that Watchdog's
// current snapshot replaced under the same configuration. c.mu must be held.
func (c *staticRefreshCoordinator) supersedesLocked(key, fingerprint, hash string) bool {
	previous, ok := c.superseded[key]
	return ok && previous.fingerprint == fingerprint && previous.hash == hash
}

// relationIsLocked reports whether the recorded relation for this configuration
// is the given one. c.mu must be held.
func (c *staticRefreshCoordinator) relationIsLocked(key, fingerprint string, relation BackendBundleRelation) bool {
	state, ok := c.relations[key]
	return ok && state.fingerprint == fingerprint && state.relation == relation
}

// setRelationLocked records the relation and reports whether it changed.
// c.mu must be held.
func (c *staticRefreshCoordinator) setRelationLocked(key, fingerprint string, relation BackendBundleRelation, debouncing bool) bool {
	previous, ok := c.relations[key]
	changed := !ok || previous.fingerprint != fingerprint || previous.relation != relation || previous.debouncing != debouncing
	c.relations[key] = backendRelationState{fingerprint: fingerprint, relation: relation, debouncing: debouncing}
	return changed
}

// lookupKeys returns the keys an entry's state may be recorded under: its own,
// then the server-scoped key, because agency entries of a server-scoped
// configuration share the campaign of the configured server entry.
func lookupKeys(server models.ObaServer) []string {
	keys := []string{server.ServerKey()}
	if server.AgencyID != "" {
		keys = append(keys, models.ServerKey(server.ObaBaseURL, ""))
	}
	return keys
}

// LastStaticFeedCheck returns the result of the entry's last completed feed
// attempt, whatever started it.
func (gs *GtfsService) LastStaticFeedCheck(server models.ObaServer) (StaticFeedCheck, bool) {
	c := gs.refreshCoordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range lookupKeys(server) {
		if state, ok := c.feedChecks[key]; ok {
			return state.check, true
		}
	}
	return StaticFeedCheck{}, false
}

// SetStaticRefreshClock replaces the clock that times backend hash runs, so
// tests in other packages need not wait out the stable duration.
func (gs *GtfsService) SetStaticRefreshClock(now func() time.Time) {
	c := gs.refreshCoordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// BackendBundleRelation returns how the backend's bundle related to
// Watchdog's snapshot at the last usable hash comparison.
func (gs *GtfsService) BackendBundleRelation(server models.ObaServer) BackendBundleRelation {
	c := gs.refreshCoordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range lookupKeys(server) {
		if state, ok := c.relations[key]; ok {
			return state.relation
		}
	}
	return BackendBundleUnknown
}

// BackendBundleDebouncing reports whether the backend's bundle relation is
// unknown while the replaced bundle's hash is being observed (a debounce run
// is in progress).
func (gs *GtfsService) BackendBundleDebouncing(server models.ObaServer) bool {
	c := gs.refreshCoordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range lookupKeys(server) {
		if state, ok := c.relations[key]; ok && state.debouncing {
			return true
		}
		if _, ok := c.superseded[key]; ok {
			if state, ok := c.relations[key]; !ok || state.relation == BackendBundleUnknown {
				return true
			}
		}
	}
	return false
}
