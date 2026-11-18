package app

import (
	"context"
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// How long a deployment waits before its metadata is read again after a
// failed read. A transient failure (network, timeout, 5xx) backs off
// exponentially from the fetch interval up to metadataTransientMaxBackoff. A
// backend that exposes no usable hash is not asked again for
// metadataUnsupportedRetryAfter.
const (
	metadataTransientMaxBackoff   = 10 * time.Minute
	metadataUnsupportedRetryAfter = time.Hour
)

// fallbackMaxRetries is the per-download retry count for a fallback campaign.
const fallbackMaxRetries = 5

// StaticBundleWatch decides, once per deployment per collection tick, how
// Watchdog keeps that deployment's static bundle current:
//
//  1. Read the backend metadata hash, unless an earlier failure put it in
//     backoff. A hash that differs from Watchdog's snapshot starts a
//     backend_changed download (gtfs.GtfsService.CheckBackendBundles).
//  2. A failed read backs off in its own BackoffStore, separate from the API
//     ping backoff: a broken metadata endpoint must never stop the rest of
//     the deployment's collection.
//  3. If the hash has not worked for gtfs.StaticFallbackEvery (a day),
//     download the bundle anyway with the fallback trigger, at most once per
//     that period.
//
// A deployment is one OBA base URL. Entries sharing it share one check.
type StaticBundleWatch struct {
	metadataBackoff *config.BackoffStore

	mu sync.Mutex
	// coveredAt is, per deployment, the last time its bundle was known to be
	// covered: when it was first seen (the startup or newcomer download covers
	// it), the last good hash, or the last fallback start.
	coveredAt map[string]time.Time
}

// NewStaticBundleWatch creates a watch whose transient metadata backoff starts
// at the collection interval.
func NewStaticBundleWatch(fetchInterval time.Duration) *StaticBundleWatch {
	return &StaticBundleWatch{
		metadataBackoff: config.NewBackoffStoreWithLimits(fetchInterval, metadataTransientMaxBackoff),
		coveredAt:       make(map[string]time.Time),
	}
}

// deploymentKey identifies a deployment by its sanitized base URL.
func deploymentKey(server models.ObaServer) string {
	return utils.SanitizeServerURL(server.ObaBaseURL)
}

// metadataDue reports whether the deployment's metadata may be read now.
func (w *StaticBundleWatch) metadataDue(key string, now time.Time) bool {
	retryAt, backingOff := w.metadataBackoff.NextRetryAt(key)
	return !backingOff || !now.Before(retryAt)
}

// recordMetadata applies the result of one metadata read.
func (w *StaticBundleWatch) recordMetadata(key string, status gtfs.BackendHashStatus, now time.Time) {
	switch status {
	case gtfs.BackendHashOK:
		w.metadataBackoff.ResetBackoff(key)
		w.mu.Lock()
		w.coveredAt[key] = now
		w.mu.Unlock()
	case gtfs.BackendHashTransient:
		w.metadataBackoff.UpdateBackoff(key)
	case gtfs.BackendHashUnsupported:
		w.metadataBackoff.DeferUntil(key, now.Add(metadataUnsupportedRetryAfter))
	case gtfs.BackendHashNotChecked:
		// Nothing was learned about the backend.
	}
}

// claimFallback reports whether a fallback download is due for the
// deployment, whose bundle has not been covered for every, and if so records
// it as started now. The first call for a deployment only starts its clock.
func (w *StaticBundleWatch) claimFallback(key string, now time.Time, every time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	coveredAt, seen := w.coveredAt[key]
	if !seen {
		w.coveredAt[key] = now
		return false
	}
	if now.Sub(coveredAt) < every {
		return false
	}
	w.coveredAt[key] = now
	return true
}

// Prune drops the state of deployments no configured server uses.
func (w *StaticBundleWatch) Prune(servers []models.ObaServer) {
	configured := make(map[string]bool, len(servers))
	for _, server := range servers {
		configured[deploymentKey(server)] = true
	}
	keep := func(key string) bool { return configured[key] }
	w.metadataBackoff.Prune(keep)
	w.mu.Lock()
	defer w.mu.Unlock()
	for key := range w.coveredAt {
		if !keep(key) {
			delete(w.coveredAt, key)
		}
	}
}

// checkDeploymentStaticBundle runs the watch for one deployment. ctx bounds
// this tick; campaignCtx outlives it and parents any download it starts.
// metadataAllowed is false while every entry of the deployment is in API ping
// backoff, which skips the metadata read but never the fallback: the fallback
// downloads from the feed URL, not from the OBA server.
func (app *Application) checkDeploymentStaticBundle(ctx, campaignCtx context.Context, servers []models.ObaServer, metadataAllowed bool) {
	if len(servers) == 0 {
		return
	}
	watch := app.StaticBundleWatch
	key := deploymentKey(servers[0])
	if metadataAllowed && watch.metadataDue(key, time.Now().UTC()) {
		status := app.GtfsService.CheckBackendBundles(ctx, campaignCtx, servers)
		watch.recordMetadata(key, status, time.Now().UTC())
	}
	every := gtfs.StaticFallbackEvery
	if ctx.Err() != nil || !watch.claimFallback(key, time.Now().UTC(), every) {
		return
	}
	app.Logger.Info("Backend static GTFS hash unusable; starting fallback static refresh",
		"server_url", key, "unusable_for_at_least", every, "trigger", gtfs.StaticRefreshFallback)
	app.GtfsService.StartStaticRefreshCampaigns(campaignCtx, servers, gtfs.StaticRefreshFallback, fallbackMaxRetries)
}
