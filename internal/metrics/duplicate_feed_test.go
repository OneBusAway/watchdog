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

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestDuplicateFeedMetricsAndRecovery(t *testing.T) {
	server := models.ObaServer{ServerName: "duplicate-feed", ObaBaseURL: "https://duplicate-feed.example", GtfsStaticFeeds: []string{"https://feeds.example/static.zip"}, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: "https://feeds.example/vehicles.pb"}}}
	labels := map[string]string{"agency_id": "", "agency_name": "", "server_name": server.ServerName, "server_url": server.ObaBaseURL, "feed": "0", "feed_url": server.GtfsRTFeeds[0].VehiclePositionURL}
	freshness := NewFeedFreshnessStore()
	failure := gtfs.FeedObservation{FeedID: "0", FeedURL: labels["feed_url"], ObservedAt: time.Now().UTC(), Success: true, DuplicateID: true}
	freshness.Observe(server, failure)
	if value, err := getMetricValue(GtfsRtFeedDuplicateID, labels); err != nil || value != 1 {
		t.Fatalf("missing realtime duplicate metric: %v, %v", value, err)
	}
	if value, err := getMetricValue(GtfsRtFeedFetchSuccess, labels); err != nil || value != 1 {
		t.Fatalf("duplicate exclusion stopped successful collection: %v, %v", value, err)
	}
	otherFailure := failure
	otherFailure.Success, otherFailure.DuplicateID = false, false
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

func TestRealtimeDuplicatesKeepOtherVehiclesMonitored(t *testing.T) {
	for _, mode := range []string{"agency", "server"} {
		t.Run(mode, func(t *testing.T) {
			feed := &gtfsrt.FeedMessage{Header: &gtfsrt.FeedHeader{GtfsRealtimeVersion: proto.String("2.0")}}
			for _, row := range []struct{ entity, vehicle, route string }{
				{"bad-1", "B:101", "B:R"},
				{"good", "A:202", "A:R"},
				{"bad-2", "B:101", "B:R"},
			} {
				feed.Entity = append(feed.Entity, &gtfsrt.FeedEntity{
					Id: proto.String(row.entity),
					Vehicle: &gtfsrt.VehiclePosition{
						Vehicle:  &gtfsrt.VehicleDescriptor{Id: proto.String(row.vehicle)},
						Trip:     &gtfsrt.TripDescriptor{RouteId: proto.String(row.route)},
						Position: &gtfsrt.Position{Latitude: proto.Float32(1), Longitude: proto.Float32(2)},
					},
				})
			}
			data, err := proto.Marshal(feed)
			if err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
			defer ts.Close()
			server := models.ObaServer{ServerName: "duplicate-" + mode, ObaBaseURL: ts.URL, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL}}}
			var agencies []models.ObaServer
			if mode == "agency" {
				server.AgencyID, server.AgencyName = "A", "Agency A"
			} else {
				a, b := server, server
				a.AgencyID, a.AgencyName = "A", "Agency A"
				b.AgencyID, b.AgencyName = "B", "Agency B"
				agencies = []models.ObaServer{a, b}
			}
			index := gtfs.NewRouteAgencyIndex()
			index.Replace(server.ServerKey(), map[string]string{"A:R": "A", "B:R": "B"}, nil, map[string]string{"A": "Agency A", "B": "Agency B"})
			store := gtfs.NewRealtimeStore()
			freshness := NewFeedFreshnessStore()
			store.SetObserver(freshness.Observe)
			service := gtfs.NewGtfsService(gtfs.NewStaticStore(), store, geo.NewBoundingBoxStore(), index, slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
			if err := service.FetchAndStoreGTFSRTFeed(context.Background(), server); err != nil {
				t.Fatal(err)
			}
			if count, err := countVehiclePositions(server, agencies, store, index); err != nil || count != 1 {
				t.Fatalf("duplicates stopped unrelated vehicle counting: %d, %v", count, err)
			}
			labels := map[string]string{"agency_id": server.AgencyID, "agency_name": server.AgencyName, "server_name": server.ServerName, "server_url": ts.URL, "feed": "0", "feed_url": ts.URL}
			if value, err := getMetricValue(GtfsRtFeedDuplicateID, labels); err != nil || value != 1 {
				t.Fatalf("duplicates were not reported: %v, %v", value, err)
			}
			if value, err := getMetricValue(GtfsRtFeedFetchSuccess, labels); err != nil || value != 1 {
				t.Fatalf("the remaining feed was not treated as successful: %v, %v", value, err)
			}
			agencyLabels := map[string]string{"agency_id": "A", "agency_name": "Agency A", "server_name": server.ServerName, "server_url": ts.URL}
			if value, err := getMetricValue(RealtimeVehiclePositions, agencyLabels); err != nil || value != 1 {
				t.Fatalf("agency A lost its unrelated vehicle metric: %v, %v", value, err)
			}
		})
	}
}
