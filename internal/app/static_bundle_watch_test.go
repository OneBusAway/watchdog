package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestStaticBundleWatchMetadataBackoff(t *testing.T) {
	// The exponential backoff is measured from the wall clock.
	now := time.Now().UTC()
	const key = "https://oba.example"
	for _, tc := range []struct {
		name      string
		status    gtfs.BackendHashStatus
		dueAfter  time.Duration // earliest moment the next read may happen
		notBefore time.Duration // a moment at which it must still be skipped
	}{
		{"ok reads every tick", gtfs.BackendHashOK, 0, 0},
		{"not checked reads every tick", gtfs.BackendHashNotChecked, 0, 0},
		{"transient backs off from the fetch interval", gtfs.BackendHashTransient, 46 * time.Second, 29 * time.Second},
		{"unsupported waits an hour", gtfs.BackendHashUnsupported, metadataUnsupportedRetryAfter, 59 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewStaticBundleWatch(30 * time.Second)
			w.recordMetadata(key, tc.status, now)
			if tc.notBefore > 0 && w.metadataDue(key, now.Add(tc.notBefore)) {
				t.Fatalf("metadata due %s after a %s result", tc.notBefore, tc.status)
			}
			if !w.metadataDue(key, now.Add(tc.dueAfter)) {
				t.Fatalf("metadata not due %s after a %s result", tc.dueAfter, tc.status)
			}
		})
	}

	t.Run("ok clears an earlier backoff", func(t *testing.T) {
		w := NewStaticBundleWatch(30 * time.Second)
		w.recordMetadata(key, gtfs.BackendHashUnsupported, now)
		w.recordMetadata(key, gtfs.BackendHashOK, now)
		if !w.metadataDue(key, now) {
			t.Fatal("a good hash left the deployment in backoff")
		}
	})
}

func TestStaticBundleWatchFallback(t *testing.T) {
	for _, every := range []time.Duration{gtfs.StaticFallbackEvery, gtfs.StaticFallbackEveryExpired} {
		t.Run(every.String(), func(t *testing.T) {
			start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			const key = "https://oba.example"
			w := NewStaticBundleWatch(30 * time.Second)

			if w.claimFallback(key, start, every) {
				t.Fatal("first sight of a deployment started a fallback; startup already downloads it")
			}
			// A working hash does not put the download off (#346).
			w.recordMetadata(key, gtfs.BackendHashOK, start.Add(every-time.Minute))
			if !w.claimFallback(key, start.Add(every), every) {
				t.Fatal("a good hash put off the periodic download")
			}
			// At most one fallback per period.
			fallbackAt := start.Add(every)
			if w.claimFallback(key, fallbackAt.Add(time.Minute), every) {
				t.Fatal("a second fallback ran inside the same period")
			}
			// A download from another trigger restarts the clock.
			w.noteDownload(key, fallbackAt.Add(every/2))
			if w.claimFallback(key, fallbackAt.Add(every), every) {
				t.Fatal("fallback ran although another trigger downloaded the feed within the period")
			}
			if !w.claimFallback(key, fallbackAt.Add(every/2+every), every) {
				t.Fatal("no fallback one period after the last download")
			}
			// An older download never moves the clock back.
			w.noteDownload(key, start)
			if w.claimFallback(key, fallbackAt.Add(every/2+every+time.Minute), every) {
				t.Fatal("an older download moved the clock back")
			}
		})
	}
}

// TestStaticBundleWatchExpiryShortensFallback checks that an expired bundle
// is downloaded every 2 hours instead of daily.
func TestStaticBundleWatchExpiryShortensFallback(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const key = "https://oba.example"
	w := NewStaticBundleWatch(30 * time.Second)
	w.claimFallback(key, start, gtfs.StaticFallbackEvery)

	at := start.Add(gtfs.StaticFallbackEveryExpired)
	if w.claimFallback(key, at, gtfs.StaticFallbackEvery) {
		t.Fatal("an unexpired bundle was downloaded after 2 hours")
	}
	if !w.claimFallback(key, at, gtfs.StaticFallbackEveryExpired) {
		t.Fatal("an expired bundle waited longer than 2 hours")
	}
}

// TestStaticBundleWatchExpiryChecksFeedAtOnce covers the review of #334: an
// expiry downloads the feed URL at once, even right after another download,
// and then every 2 hours, hash or no hash.
func TestStaticBundleWatchExpiryChecksFeedAtOnce(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const key = "https://oba.example"
	w := NewStaticBundleWatch(30 * time.Second)
	w.claimFallback(key, start, gtfs.StaticFallbackEvery)
	w.noteDownload(key, start.Add(time.Minute))

	w.observeExpiry(key, true)
	w.noteDownload(key, start.Add(2*time.Minute)) // a download before the expiry check must not cancel it
	if !w.claimFallback(key, start.Add(3*time.Minute), gtfs.StaticFallbackEveryExpired) {
		t.Fatal("an expiry did not check the feed URL at once")
	}
	w.observeExpiry(key, true) // still expired: no second immediate check
	if w.claimFallback(key, start.Add(4*time.Minute), gtfs.StaticFallbackEveryExpired) {
		t.Fatal("a continuing expiry checked the feed again at once")
	}
	w.recordMetadata(key, gtfs.BackendHashOK, start.Add(time.Hour))
	if !w.claimFallback(key, start.Add(3*time.Minute+gtfs.StaticFallbackEveryExpired), gtfs.StaticFallbackEveryExpired) {
		t.Fatal("a matching hash held back the 2-hour check of an expired bundle")
	}
}

func TestBundleExpiryReporterDeploymentExpired(t *testing.T) {
	r := NewBundleExpiryReporter()
	now := time.Now().UTC()
	present := func(string) bool { return true }
	r.update(models.ServerKey("https://a.example", "x"), withholdExpired, time.Time{}, now)
	r.update(models.ServerKey("https://b.example", "y"), withholdNone, time.Time{}, now)
	r.update(models.ServerKey("https://c.example", "z"), withholdBackendBehind, time.Time{}, now)
	if !r.deploymentExpired("https://a.example", present) {
		t.Fatal("deployment with an expired agency not reported expired")
	}
	if r.deploymentExpired("https://b.example", present) || r.deploymentExpired("https://c.example", present) || r.deploymentExpired("https://d.example", present) {
		t.Fatal("deployment without an expired agency reported expired")
	}
}

// TestBundleExpiryReporterForgetsRemovedAgency checks that an expired agency
// that left the merged bundle no longer keeps its deployment expired.
func TestBundleExpiryReporterForgetsRemovedAgency(t *testing.T) {
	r := NewBundleExpiryReporter()
	gone := models.ServerKey("https://a.example", "gone")
	r.update(gone, withholdExpired, time.Time{}, time.Now().UTC())
	if r.deploymentExpired("https://a.example", func(key string) bool { return key != gone }) {
		t.Fatal("an agency without a snapshot kept the deployment expired")
	}
	if r.entry(gone).kind != withholdNone {
		t.Fatal("the removed agency's record was kept")
	}
}

func TestStaticBundleWatchPrune(t *testing.T) {
	now := time.Now().UTC()
	kept := models.ObaServer{ServerName: "kept", ObaBaseURL: "https://kept.example"}
	gone := models.ObaServer{ServerName: "gone", ObaBaseURL: "https://gone.example"}
	w := NewStaticBundleWatch(30 * time.Second)
	for _, server := range []models.ObaServer{kept, gone} {
		w.recordMetadata(deploymentKey(server), gtfs.BackendHashUnsupported, now)
		w.claimFallback(deploymentKey(server), now, gtfs.StaticFallbackEvery)
	}
	w.Prune([]models.ObaServer{kept})
	if !w.metadataDue(deploymentKey(gone), now) {
		t.Fatal("a departed deployment kept its metadata backoff")
	}
	if _, ok := w.downloadedAt[deploymentKey(gone)]; ok {
		t.Fatal("a departed deployment kept its fallback clock")
	}
	if w.metadataDue(deploymentKey(kept), now) {
		t.Fatal("pruning dropped a configured deployment's backoff")
	}
}

// TestCollectionSkipsMetadataWhileBackingOff checks the tick loop honours the
// metadata backoff instead of asking again on every tick.
func TestCollectionSkipsMetadataWhileBackingOff(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/metadata.json":
			requests.Add(1)
			http.NotFound(w, r)
		case "/api/where/current-time.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"currentTime":1234567890000,"data":{"entry":{"readableTime":"now"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	app := newEmptyApplication(t)
	server := models.ObaServer{ServerName: "backoff-test", AgencyID: "A", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{"unused.zip"}}
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{server})
	app.GtfsService.StaticStore.SetConfiguredServers([]models.ObaServer{server})

	for range 3 {
		app.collectOnce(context.Background())
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("metadata requests = %d over three ticks, want 1: an unsupported backend must wait", got)
	}
	// Collection itself is unaffected by the metadata backoff.
	if _, backingOff := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey()); backingOff {
		t.Fatal("a metadata failure put the server's API ping in backoff")
	}
}
