package gtfs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

// TestRefreshGTFSBundlesReadsLiveConfig covers a stale-config bug: the refresh
// routine was started with the server slice captured at boot, so a server
// added by a later --config-url refresh never had its bundle downloaded — its
// static-derived metrics stayed empty until the process restarted.
func TestRefreshGTFSBundlesReadsLiveConfig(t *testing.T) {
	// Only supplier updates are under test; a tiny feed avoids spending the
	// next-tick deadline parsing the large fixture under race instrumentation.
	bundle := smallStaticFeed(t)

	var (
		shuttingDown  atomic.Bool
		firstTickOnce sync.Once
		addedOnce     sync.Once
		firstTick     = make(chan struct{})
		sawAdded      = make(chan struct{})
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var signal chan struct{}
		switch r.URL.Path {
		case "/initial.zip":
			signal = firstTick
		case "/added.zip":
			signal = sawAdded
		}
		if _, err := w.Write(bundle); err != nil && !shuttingDown.Load() {
			t.Errorf("write GTFS fixture: %v", err)
		}
		if signal == firstTick {
			firstTickOnce.Do(func() { close(firstTick) })
		} else if signal == sawAdded {
			addedOnce.Do(func() { close(sawAdded) })
		}
	}))

	// The supplier starts out with one server and gains a second one, exactly
	// as a config refresh would.
	var suppliedMu sync.Mutex
	added := false
	servers := func() []models.ObaServer {
		suppliedMu.Lock()
		defer suppliedMu.Unlock()
		list := []models.ObaServer{{
			ServerName:      "initial",
			AgencyID:        "A",
			ObaBaseURL:      ts.URL,
			GtfsStaticFeeds: []string{ts.URL + "/initial.zip"},
		}}
		if added {
			list = append(list, models.ObaServer{
				ServerName:      "added",
				AgencyID:        "A",
				ObaBaseURL:      ts.URL + "/other",
				GtfsStaticFeeds: []string{ts.URL + "/added.zip"},
			})
		}
		return list
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() {
		shuttingDown.Store(true)
		cancel()
		<-done
		ts.Close()
		http.DefaultClient.CloseIdleConnections()
	}()

	go func() {
		defer close(done)
		refreshGTFSBundles(ctx, ts.Client(), servers, slog.New(slog.NewTextHandler(io.Discard, nil)),
			10*time.Millisecond, geo.NewBoundingBoxStore(), NewStaticStore(), NewRouteAgencyIndex(), nil, nil, nil, 1)
	}()

	// Wait for a tick that used the original list before changing it, so a
	// routine that snapshots the config at start-up has definitely taken its
	// snapshot by the time the second server appears.
	select {
	case <-firstTick:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never fetched the initial server")
	}

	suppliedMu.Lock()
	added = true
	suppliedMu.Unlock()

	select {
	case <-sawAdded:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never fetched the server added after start; it is holding the boot-time config")
	}
}
