package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"strings"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

func TestCollectionChecksMetadataOncePerBackend(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/metadata.json" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer ts.Close()
	app := newEmptyApplication(t)
	server := models.ObaServer{ServerName: "metadata-test", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{"unused.zip"}}
	// Two configured entries share the deployment. Neither has static agencies
	// yet, but metadata must still be checked on every tick.
	server.AgencyID = "A"
	sibling := server
	sibling.AgencyID = "B"
	entries := []models.ObaServer{server, sibling}
	app.ConfigService.Config.UpdateConfig(entries)
	app.GtfsService.StaticStore.SetConfiguredServers(entries)
	for _, entry := range entries {
		app.ConfigService.BackoffStore.UpdateBackoff(entry.ServerKey())
	}
	app.collectOnce(context.Background())
	app.collectOnce(context.Background())
	if requests.Load() != 2 {
		t.Fatalf("metadata requests = %d, want one per tick", requests.Load())
	}
}

func TestSlowMetadataDoesNotDelayNormalCollection(t *testing.T) {
	started := make(chan struct{})
	ping := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/metadata.json" {
			close(started)
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/api/where/current-time.json" {
			select {
			case ping <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":200,"currentTime":1234567890000,"data":{"entry":{"readableTime":"now"}}}`))
	}))
	defer ts.Close()
	app := newEmptyApplication(t)
	server := models.ObaServer{ServerName: "slow-metadata", AgencyID: "A", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{"unused.zip"}}
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{server})
	app.GtfsService.StaticStore.SetConfiguredServers([]models.ObaServer{server})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.collectOnce(ctx) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("metadata request never started")
	}
	select {
	case <-ping:
	case <-time.After(time.Second):
		t.Fatal("normal collection blocked on metadata")
	}
	cancel()
	<-done
	finished = true
	// An unavailable metadata endpoint must not publish a mismatch.
	if _, found := gaugeValueFor(metrics.GtfsStaticBackendHashMatch, map[string]string{"server_url": ts.URL}); found {
		t.Fatal("unavailable metadata exported a mismatch")
	}
}

func TestBackendHashGaugeLifecycleAndAPIHealth(t *testing.T) {
	// This test exercises hash publication, not large-feed parsing. Keep the
	// fixture tiny so race instrumentation cannot exhaust the startup deadline.
	bundle := backendHashTestFeed(t)
	sum := sha256.Sum256(bundle)
	var backendHash atomic.Value
	backendHash.Store(hex.EncodeToString(sum[:]))
	var status atomic.Int32
	status.Store(http.StatusOK)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed.zip" {
			w.Write(bundle)
			return
		}
		w.WriteHeader(int(status.Load()))
		fmt.Fprintf(w, `{"staticGtfsHash":%q}`, backendHash.Load().(string))
	}))
	defer ts.Close()
	app := newEmptyApplication(t)
	server := models.ObaServer{ServerName: "gauge-lifecycle", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/feed.zip"}}
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{server})
	app.GtfsService.StaticStore.SetConfiguredServers([]models.ObaServer{server})
	defer metrics.DeleteSeriesForServer(ts.URL)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	check := func() { app.GtfsService.CheckBackendBundles(context.Background(), stopped, []models.ObaServer{server}) }
	assertGauge := func(want float64, present bool) {
		t.Helper()
		value, found := gaugeValueFor(metrics.GtfsStaticBackendHashMatch, map[string]string{"server_url": ts.URL})
		if found != present || (found && value != want) {
			t.Fatalf("hash gauge = %v, present=%v; want %v, present=%v", value, found, want, present)
		}
	}
	check()
	assertGauge(0, false)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	// Preserve the existing metrics observer while retaining feed failure
	// diagnostics for a timeout, without sharing mutable state with the worker.
	feedDiagnostics := make(chan gtfs.StaticFeedRefreshObservation, 16)
	reportFeed := app.MetricsService.StaticFeedRefreshObserver()
	app.GtfsService.SetStaticFeedRefreshObserver(func(server models.ObaServer, feedURL string, observation gtfs.StaticFeedRefreshObservation) {
		reportFeed(server, feedURL, observation)
		select {
		case feedDiagnostics <- observation:
		default:
		}
	})
	app.GtfsService.StartStaticRefreshCampaigns(ctx, []models.ObaServer{server}, gtfs.StaticRefreshStartup, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, ok := app.GtfsService.StaticStore.GetRefreshState(server)
		if ok && state.Success {
			break
		}
		if time.Now().After(deadline) {
			var latestFeed gtfs.StaticFeedRefreshObservation
			draining := true
			for draining {
				select {
				case latestFeed = <-feedDiagnostics:
				default:
					draining = false
				}
			}
			t.Fatalf("startup publish did not finish: refresh_state=%+v present=%t latest_feed=%+v", state, ok, latestFeed)
		}
		time.Sleep(time.Millisecond)
	}
	check()
	assertGauge(1, true)
	backendHash.Store(strings.Repeat("a", 64))
	check()
	assertGauge(0, true)
	metrics.ObaApiStatus.WithLabelValues(server.ServerName, ts.URL).Set(1)
	app.ConfigService.BackoffStore.UpdateBackoff(server.ServerKey())
	before, exists := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey())
	if !exists {
		t.Fatal("backoff not seeded")
	}
	for _, code := range []int{404, 503} {
		status.Store(int32(code))
		check()
		assertGauge(0, false)
		after, ok := app.ConfigService.BackoffStore.NextRetryAt(server.ServerKey())
		if !ok || !after.Equal(before) {
			t.Fatal("metadata failure changed backoff")
		}
		value, found := gaugeValueFor(metrics.ObaApiStatus, map[string]string{"server_url": ts.URL})
		if !found || value != 1 {
			t.Fatal("metadata failure changed API status")
		}
	}
	status.Store(http.StatusOK)
	backendHash.Store(hex.EncodeToString(sum[:]))
	check()
	assertGauge(1, true)
}

// backendHashTestFeed builds a minimal valid feed for the gauge publication test.
func backendHashTestFeed(t *testing.T) []byte {
	t.Helper()
	files := map[string]string{
		"agency.txt":     "agency_id,agency_name,agency_url,agency_timezone\n40,Sound Transit,https://example.com,UTC\n",
		"routes.txt":     "route_id,agency_id,route_short_name,route_type\nR,40,R,3\n",
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,1,1,20260101,20261231\n",
		"trips.txt":      "route_id,service_id,trip_id\nR,WK,T\n",
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,1\nS2,Two,2,2\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,2\n",
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
