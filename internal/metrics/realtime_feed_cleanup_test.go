package metrics

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestRealtimeFeedRemovalDeletesTransitionCounters(t *testing.T) {
	server := models.ObaServer{
		ServerName: "counter-cleanup", AgencyID: "a", AgencyName: "Agency A", ObaBaseURL: "https://counter-cleanup.example",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "https://feeds.example/rt.pb"}},
	}
	realtime := gtfs.NewRealtimeStore()
	service := NewMetricsService(gtfs.NewStaticStore(), realtime, geo.NewBoundingBoxStore(), gtfs.NewRouteAgencyIndex(), NewVehicleLastSeen(), NewUnmatchedStopTracker(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	service.FeedFreshness.Observe(server, gtfs.FeedObservation{FeedID: "0", FeedURL: server.GtfsRTFeeds[0].VehiclePositionURL, Success: true, ObservedAt: time.Now(), PayloadHash: "hash"})
	labels := []string{server.AgencyID, server.AgencyName, server.ServerName, server.ObaBaseURL, "0"}
	GtfsRtVehicleSourceTimestampAdvances.WithLabelValues(labels...).Inc()
	GtfsRtVehicleStateChanges.WithLabelValues(labels...).Inc()

	server.GtfsRTFeeds = nil
	realtime.ReconcileConfiguration(server)

	if got := seriesMatching(GtfsRtVehicleSourceTimestampAdvances, map[string]string{"server_url": server.ObaBaseURL, "feed": "0"}); len(got) != 0 {
		t.Fatalf("source timestamp counter retained %d removed-feed series", len(got))
	}
	if got := seriesMatching(GtfsRtVehicleStateChanges, map[string]string{"server_url": server.ObaBaseURL, "feed": "0"}); len(got) != 0 {
		t.Fatalf("state counter retained %d removed-feed series", len(got))
	}
}
