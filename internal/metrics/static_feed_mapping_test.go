package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestStaticFeedMappingStoreReconcilesSuccessfulResults(t *testing.T) {
	server := models.ObaServer{ServerName: "mapping-test", ObaBaseURL: "https://mapping-test.example.com"}
	feedURL := "https://feeds.example.com/static.zip"
	store := NewStaticFeedMappingStore()
	store.Observe(server, gtfs.StaticFeedMappingObservation{
		ConfiguredFeedURLs: []string{feedURL},
		Results:            []gtfs.StaticFeedMappingResult{{FeedURL: feedURL, Mappings: []gtfs.StaticFeedAgencyMapping{{AgencyID: "old", AgencyName: "Old"}}}},
	})
	oldLabels := prometheus.Labels{"feed_url": feedURL, "agency_id": "old", "agency_name": "Old", "server_name": server.ServerName, "server_url": server.ObaBaseURL}
	if !seriesExists(GtfsStaticFeedAgencyMappingInfo, oldLabels) {
		t.Fatal("expected initial proven mapping series")
	}

	store.Observe(server, gtfs.StaticFeedMappingObservation{
		ConfiguredFeedURLs: []string{feedURL},
		Results:            []gtfs.StaticFeedMappingResult{{FeedURL: feedURL, Failures: []gtfs.StaticFeedMappingFailure{{Reason: "unknown_agency"}}}},
	})
	if seriesExists(GtfsStaticFeedAgencyMappingInfo, oldLabels) {
		t.Fatal("expected changed relationship to retire the old info series")
	}
	failureLabels := prometheus.Labels{"feed_url": feedURL, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "reason": "unknown_agency"}
	if !seriesExists(GtfsStaticFeedAgencyMappingFailure, failureLabels) {
		t.Fatal("expected replacement failure series")
	}

	// No result means this configured feed failed to refresh. Keep its last
	// successfully parsed classification rather than inventing a mapping error.
	store.Observe(server, gtfs.StaticFeedMappingObservation{ConfiguredFeedURLs: []string{feedURL}})
	if !seriesExists(GtfsStaticFeedAgencyMappingFailure, failureLabels) {
		t.Fatal("expected a failed download to preserve the last parsed classification")
	}

	store.Observe(server, gtfs.StaticFeedMappingObservation{})
	if seriesExists(GtfsStaticFeedAgencyMappingFailure, failureLabels) {
		t.Fatal("expected a feed removed from config to retire its failure series")
	}
}

func TestStaticFeedMappingStorePruneRetiresFailureWithoutAgencyLabel(t *testing.T) {
	server := models.ObaServer{ServerName: "mapping-prune", ObaBaseURL: "https://mapping-prune.example.com", AgencyID: "A"}
	feedURL := "https://feeds.example.com/prune.zip"
	store := NewStaticFeedMappingStore()
	store.Observe(server, gtfs.StaticFeedMappingObservation{
		ConfiguredFeedURLs: []string{feedURL},
		Results:            []gtfs.StaticFeedMappingResult{{FeedURL: feedURL, Failures: []gtfs.StaticFeedMappingFailure{{Reason: "ambiguous_agency"}}}},
	})
	labels := prometheus.Labels{"feed_url": feedURL, "server_name": server.ServerName, "server_url": server.ObaBaseURL, "reason": "ambiguous_agency"}
	store.Prune(func(string) bool { return false })
	if seriesExists(GtfsStaticFeedAgencyMappingFailure, labels) {
		t.Fatal("expected server pruning to retire failure series with no agency_id label")
	}
}
