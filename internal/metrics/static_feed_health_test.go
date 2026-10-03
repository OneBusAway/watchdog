package metrics

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestStaticFeedHealthPreservesSuccessAndRetiresChangedLabels(t *testing.T) {
	server := models.ObaServer{ServerName: "feed-health", ObaBaseURL: "https://feed-health.example", AgencyID: "a", GtfsStaticFeeds: []string{"https://feeds.example/static.zip"}}
	feedURL := server.GtfsStaticFeeds[0]
	store := NewStaticFeedHealthStore()
	successAt := time.Unix(1000, 0).UTC()
	store.Observe(server, feedURL, gtfs.StaticFeedRefreshObservation{AttemptedAt: successAt, Success: true})

	failureAt := successAt.Add(time.Hour)
	store.Observe(server, feedURL, gtfs.StaticFeedRefreshObservation{
		AttemptedAt: failureAt, FailureSince: failureAt,
		Stage: gtfs.StaticFailureHTTP, Reason: gtfs.StaticReasonServerError,
	})
	labels := map[string]string{"feed_url": feedURL, "server_name": server.ServerName, "server_url": server.ObaBaseURL}
	if got, _ := getMetricValue(GtfsStaticFeedFetchSuccess, labels); got != 0 {
		t.Fatalf("fetch success = %v", got)
	}
	if got, _ := getMetricValue(GtfsStaticFeedLastSuccessfulFetch, labels); got != float64(successAt.Unix()) {
		t.Fatalf("last successful fetch = %v", got)
	}
	if got := seriesMatching(GtfsStaticFeedFailureInfo, map[string]string{"server_url": server.ObaBaseURL, "reason": string(gtfs.StaticReasonServerError)}); len(got) != 1 {
		t.Fatalf("server-error info series = %d", len(got))
	}

	store.Observe(server, feedURL, gtfs.StaticFeedRefreshObservation{
		AttemptedAt: failureAt.Add(time.Minute), FailureSince: failureAt,
		Stage: gtfs.StaticFailureHTTP, Reason: gtfs.StaticReasonNotFound, Conservative: true,
	})
	if got := seriesMatching(GtfsStaticFeedFailureInfo, map[string]string{"server_url": server.ObaBaseURL, "reason": string(gtfs.StaticReasonServerError)}); len(got) != 0 {
		t.Fatalf("old failure classification remained: %d", len(got))
	}
	if got, _ := getMetricValue(GtfsStaticFeedConservativeMode, labels); got != 1 {
		t.Fatalf("conservative mode = %v", got)
	}

	store.Reconcile(nil)
	if got := seriesMatching(GtfsStaticFeedFetchSuccess, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("removed feed retained %d health series", len(got))
	}
	if got := seriesMatching(GtfsStaticFeedFailureInfo, map[string]string{"server_url": server.ObaBaseURL}); len(got) != 0 {
		t.Fatalf("removed feed retained %d failure series", len(got))
	}
}
