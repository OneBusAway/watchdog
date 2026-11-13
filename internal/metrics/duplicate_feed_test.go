package metrics

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestDuplicateFeedMetricsAndRecovery(t *testing.T) {
	server := models.ObaServer{ServerName: "duplicate-feed", ObaBaseURL: "https://duplicate-feed.example", GtfsStaticFeeds: []string{"https://feeds.example/static.zip"}, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "https://feeds.example/vehicles.pb"}}}
	labels := map[string]string{"agency_id": "", "agency_name": "", "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0", "feed_url": server.GtfsRTFeeds[0].VehiclePositionURL}
	freshness := NewFeedFreshnessStore()
	failure := gtfs.FeedObservation{FeedID: "0", FeedURL: labels["feed_url"], ObservedAt: time.Now().UTC(), DuplicateID: true}
	freshness.Observe(server, failure)
	if value, err := getMetricValue(GtfsRtFeedDuplicateID, labels); err != nil || value != 1 {
		t.Fatalf("missing realtime duplicate metric: %v, %v", value, err)
	}
	if value, err := getMetricValue(GtfsRtFeedFetchSuccess, labels); err != nil || value != 0 {
		t.Fatalf("duplicate feed reported as successful: %v, %v", value, err)
	}
	otherFailure := failure
	otherFailure.DuplicateID = false
	freshness.Observe(server, otherFailure)
	if value, err := getMetricValue(GtfsRtFeedDuplicateID, labels); err != nil || value != 0 {
		t.Fatalf("a different fetch failure retained the duplicate flag: %v, %v", value, err)
	}
	if value, err := getMetricValue(GtfsRtFeedFetchSuccess, labels); err != nil || value != 0 {
		t.Fatalf("a different fetch failure reported success: %v, %v", value, err)
	}
	recovery := failure
	recovery.Success, recovery.DuplicateID = true, false
	freshness.Observe(server, recovery)
	if value, err := getMetricValue(GtfsRtFeedDuplicateID, labels); err != nil || value != 0 {
		t.Fatalf("recovery did not clear duplicate metric: %v, %v", value, err)
	}

	health := NewStaticFeedHealthStore()
	health.Observe(server, server.GtfsStaticFeeds[0], gtfs.StaticFeedRefreshObservation{
		AttemptedAt: failure.ObservedAt, FailureSince: failure.ObservedAt,
		Stage: gtfs.StaticFailureParse, Reason: gtfs.StaticReasonDuplicateID,
	})
	staticLabels := map[string]string{"feed_url": server.GtfsStaticFeeds[0], "server_name": server.ServerName, "server_url": server.ObaBaseURL, "stage": "parse", "reason": "duplicate_id"}
	if value, err := getMetricValue(GtfsStaticFeedFailureInfo, staticLabels); err != nil || value != 1 {
		t.Fatalf("missing static duplicate metric: %v, %v", value, err)
	}
	health.Observe(server, server.GtfsStaticFeeds[0], gtfs.StaticFeedRefreshObservation{AttemptedAt: time.Now().UTC(), Success: true})
	if got := seriesMatching(GtfsStaticFeedFailureInfo, staticLabels); len(got) != 0 {
		t.Fatal("static duplicate metric survived recovery")
	}

	server.GtfsRTFeeds[0].VehiclePositionURL = "https://feeds.example/replacement.pb"
	freshness.Reconcile(server)
	if got := seriesMatching(GtfsRtFeedDuplicateID, labels); len(got) != 0 {
		t.Fatal("duplicate metric survived source replacement")
	}
}

func TestSynchronousStaticRefreshClearsDuplicateFailureOnRecovery(t *testing.T) {
	var invalid bytes.Buffer
	archive := zip.NewWriter(&invalid)
	file, err := archive.Create("stops.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,2\nS1,One,1,2\n"); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	valid := readFixture(t, "gtfs.zip")
	var broken atomic.Bool
	broken.Store(true)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if broken.Load() {
			_, _ = w.Write(invalid.Bytes())
		} else {
			_, _ = w.Write(valid)
		}
	}))
	defer ts.Close()
	server := models.ObaServer{ServerName: "synchronous-recovery", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL}}
	store := gtfs.NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	service := gtfs.NewGtfsService(store, gtfs.NewRealtimeStore(), geo.NewBoundingBoxStore(), gtfs.NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	health := NewStaticFeedHealthStore()
	service.SetStaticFeedRefreshObserver(health.Observe)
	labels := map[string]string{"feed_url": ts.URL, "server_name": server.ServerName, "server_url": ts.URL}
	service.DownloadGTFSBundles(context.Background(), []models.ObaServer{server}, 1)
	if got, err := getMetricValue(GtfsStaticFeedFetchSuccess, labels); err != nil || got != 0 {
		t.Fatalf("duplicate feed did not report failure: %v, %v", got, err)
	}
	if series := seriesMatching(GtfsStaticFeedFailureInfo, map[string]string{"server_url": ts.URL, "reason": "duplicate_id"}); len(series) != 1 {
		t.Fatalf("missing duplicate failure series: %d", len(series))
	}
	broken.Store(false)
	service.DownloadGTFSBundles(context.Background(), []models.ObaServer{server}, 1)
	if _, ok := store.Get(server.ServerKey()); !ok {
		t.Fatal("corrected artifact was not published")
	}
	if got, err := getMetricValue(GtfsStaticFeedFetchSuccess, labels); err != nil || got != 1 {
		t.Fatalf("successful retry left the feed marked as failed: %v, %v", got, err)
	}
	if series := seriesMatching(GtfsStaticFeedFailureInfo, map[string]string{"server_url": ts.URL}); len(series) != 0 {
		t.Fatal("successful retry retained a failure series")
	}
}
