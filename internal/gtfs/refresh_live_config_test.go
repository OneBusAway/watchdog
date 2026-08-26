package gtfs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
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
	bundle, err := os.ReadFile("../../testdata/gtfs.zip")
	if err != nil {
		t.Fatalf("read GTFS fixture: %v", err)
	}

	var (
		firstTickOnce sync.Once
		addedOnce     sync.Once
		firstTick     = make(chan struct{})
		sawAdded      = make(chan struct{})
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/initial.zip":
			firstTickOnce.Do(func() { close(firstTick) })
		case "/added.zip":
			addedOnce.Do(func() { close(sawAdded) })
		}
		if _, err := w.Write(bundle); err != nil {
			t.Errorf("write GTFS fixture: %v", err)
		}
	}))
	defer ts.Close()
	defer http.DefaultClient.CloseIdleConnections()

	// The supplier starts out with one server and gains a second one, exactly
	// as a config refresh would.
	var suppliedMu sync.Mutex
	added := false
	servers := func() []models.ObaServer {
		suppliedMu.Lock()
		defer suppliedMu.Unlock()
		list := []models.ObaServer{{
			ServerName:      "initial",
			AgencyID:        "agency-a",
			ObaBaseURL:      ts.URL,
			GtfsStaticFeeds: []string{ts.URL + "/initial.zip"},
		}}
		if added {
			list = append(list, models.ObaServer{
				ServerName:      "added",
				AgencyID:        "agency-b",
				ObaBaseURL:      ts.URL + "/other",
				GtfsStaticFeeds: []string{ts.URL + "/added.zip"},
			})
		}
		return list
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go refreshGTFSBundles(ctx, ts.Client(), servers, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Millisecond, geo.NewBoundingBoxStore(), NewStaticStore(), NewRouteAgencyIndex(), nil, 1)

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
