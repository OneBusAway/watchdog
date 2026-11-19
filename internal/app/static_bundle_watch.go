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
//  3. Every deployment's feed URL is downloaded at least once a day, whatever
//     the hash says. If no download of the feed URL has started for
//     gtfs.StaticFallbackEvery (a day), whatever started the last one, a
//     download with the fallback trigger starts. A good hash does not put it
//     off: the hash only shows that Watchdog and the backend agree, not that
//     either is current, and a daily download is what recovers a schedule
//     left unavailable by a failed campaign, a failed startup download, or a
//     backend that never loads a newer bundle. A byte-identical download is
//     only hashed, not parsed or published again.
//  4. Once the deployment's bundle has expired, download it at once and then
//     every gtfs.StaticFallbackEveryExpired (2 hours).
//
// A deployment is one OBA base URL. Entries sharing it share one check.
type StaticBundleWatch struct {
	metadataBackoff *config.BackoffStore

	mu sync.Mutex
	// downloadedAt is, per deployment, when a download of its feed URL last
	// started: the first time the deployment was seen (the startup or
	// newcomer download covers it), the last fallback start, or the last
	// feed download from any other trigger (noteDownload).
	downloadedAt map[string]time.Time
	// expired records the deployments last seen with an expired bundle.
	expired map[string]bool
	// checkNow records deployments whose bundle has just expired, so the next
	// claimFallback starts a download whatever the clock says.
	checkNow map[string]bool
}

// NewStaticBundleWatch creates a watch whose transient metadata backoff starts
// at the collection interval.
func NewStaticBundleWatch(fetchInterval time.Duration) *StaticBundleWatch {
	return &StaticBundleWatch{
		metadataBackoff: config.NewBackoffStoreWithLimits(fetchInterval, metadataTransientMaxBackoff),
		downloadedAt:    make(map[string]time.Time),
		expired:         make(map[string]bool),
		checkNow:        make(map[string]bool),
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

// recordMetadata applies the result of one metadata read to the metadata
// backoff. It never moves the daily download: see StaticBundleWatch.
func (w *StaticBundleWatch) recordMetadata(key string, status gtfs.BackendHashStatus, now time.Time) {
	switch status {
	case gtfs.BackendHashOK:
		w.metadataBackoff.ResetBackoff(key)
	case gtfs.BackendHashTransient:
		w.metadataBackoff.UpdateBackoff(key)
	case gtfs.BackendHashUnsupported:
		w.metadataBackoff.DeferUntil(key, now.Add(metadataUnsupportedRetryAfter))
	case gtfs.BackendHashNotChecked:
		// Nothing was learned about the backend.
	}
}

// observeExpiry records whether the deployment's bundle is expired. When it
// has just expired, the next claimFallback is due at once, so the feed URL is
// checked before the expiry is reported.
func (w *StaticBundleWatch) observeExpiry(key string, expired bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !expired {
		delete(w.expired, key)
		return
	}
	if !w.expired[key] {
		w.expired[key] = true
		w.checkNow[key] = true
	}
}

// noteDownload records that a download of the deployment's feed URL started
// at, whatever started it, so the daily download counts from it.
func (w *StaticBundleWatch) noteDownload(key string, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if at.After(w.downloadedAt[key]) {
		w.downloadedAt[key] = at
	}
}

// claimFallback reports whether a fallback download is due for the
// deployment, whose feed URL has not been downloaded for every (or whose
// bundle has just expired), and if so records it as started now. The first
// call for a deployment only starts its clock.
func (w *StaticBundleWatch) claimFallback(key string, now time.Time, every time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.checkNow[key] {
		delete(w.checkNow, key)
		w.downloadedAt[key] = now
		return true
	}
	downloadedAt, seen := w.downloadedAt[key]
	if !seen {
		w.downloadedAt[key] = now
		return false
	}
	if now.Sub(downloadedAt) < every {
		return false
	}
	w.downloadedAt[key] = now
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
	for _, m := range []map[string]bool{w.expired, w.checkNow} {
		for key := range m {
			if !keep(key) {
				delete(m, key)
			}
		}
	}
	for key := range w.downloadedAt {
		if !keep(key) {
			delete(w.downloadedAt, key)
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
	expired := app.BundleExpiry.deploymentExpired(key, app.hasStaticSnapshot)
	watch.observeExpiry(key, expired)
	if metadataAllowed && watch.metadataDue(key, time.Now().UTC()) {
		status := app.GtfsService.CheckBackendBundles(ctx, campaignCtx, servers)
		watch.recordMetadata(key, status, time.Now().UTC())
	}
	// A download any other trigger started (startup, backend_changed, a
	// configuration change) counts toward the daily one. The oldest entry
	// counts, so every entry of the deployment is checked daily.
	if at, ok := app.oldestFeedDownload(servers); ok {
		watch.noteDownload(key, at)
	}
	every := gtfs.StaticFallbackEvery
	if expired {
		every = gtfs.StaticFallbackEveryExpired
	}
	if ctx.Err() != nil || !watch.claimFallback(key, time.Now().UTC(), every) {
		return
	}
	if expired {
		app.Logger.Info("Static GTFS bundle expired; checking the feed URL with a fallback static refresh",
			"server_url", key, "every", every, "trigger", gtfs.StaticRefreshFallback)
	} else {
		app.Logger.Info("Daily static GTFS check; downloading the feed URL",
			"server_url", key, "not_downloaded_for_at_least", every, "trigger", gtfs.StaticRefreshFallback)
	}
	app.GtfsService.StartStaticRefreshCampaigns(campaignCtx, servers, gtfs.StaticRefreshFallback, fallbackMaxRetries)
}

// oldestFeedDownload returns the oldest of the entries' last feed downloads.
// Entries without one yet are skipped: the deployment's first sighting starts
// their clock.
func (app *Application) oldestFeedDownload(servers []models.ObaServer) (time.Time, bool) {
	var oldest time.Time
	found := false
	for _, server := range servers {
		check, ok := app.GtfsService.LastStaticFeedCheck(server)
		if !ok {
			continue
		}
		if !found || check.At.Before(oldest) {
			oldest, found = check.At, true
		}
	}
	return oldest, found
}

// hasStaticSnapshot reports whether a static snapshot is stored under the key.
func (app *Application) hasStaticSnapshot(serverKey string) bool {
	_, ok := app.GtfsService.StaticStore.Get(serverKey)
	return ok
}
