package metrics

import (
	"testing"
	"time"

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
