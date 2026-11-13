package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"github.com/getsentry/sentry-go"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestUpstreamMergedFeedPreservesIDsAndAttribution(t *testing.T) {
	files := map[string]string{
		"agency.txt":   "agency_id,agency_name,agency_url,agency_timezone\nnorth:A,North,https://north.example,UTC\nsouth:A,South,https://south.example,UTC\n",
		"routes.txt":   "route_id,agency_id,route_short_name,route_type\nnorth:R,north:A,R,3\nsouth:R,south:A,R,3\n",
		"trips.txt":    "route_id,service_id,trip_id\nnorth:R,shared_service,north:T\nsouth:R,shared_service,south:T\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nshared_service,1,1,1,1,1,1,1,20260101,20261231\n",
		// Co-located stops remain distinct when the upstream artifact gives them different IDs.
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon\nnorth:S,North stop,1,2\nsouth:S,South stop,1,2\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nnorth:T,08:00:00,08:00:00,north:S,1\nnorth:T,09:00:00,09:00:00,north:S,2\nsouth:T,08:00:00,08:00:00,south:S,1\nsouth:T,09:00:00,09:00:00,south:S,2\n",
	}
	ts := serveBytes(t, makeScheduleZip(t, files))
	defer ts.Close()
	server := scheduleServer("", ts.URL)
	store, realtime, index := NewStaticStore(), NewRealtimeStore(), NewRouteAgencyIndex()
	service := NewGtfsService(store, realtime, geo.NewBoundingBoxStore(), index, slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	service.DownloadGTFSBundles(context.Background(), []models.ObaServer{server}, 1)
	for _, prefix := range []string{"north:", "south:"} {
		key := models.ServerKey(server.ObaBaseURL, prefix+"A")
		data, ok := store.Get(key)
		if !ok || len(data.Stops) != 2 || len(data.Routes) != 2 || len(data.Services) != 1 {
			t.Fatalf("missing complete merged snapshot for %s: %+v", key, data)
		}
		stops, err := GetStopLocationsByIDs(key, []string{prefix + "S"}, store)
		if err != nil || stops[prefix+"S"].Id != prefix+"S" {
			t.Fatalf("stop ID changed: %+v, %v", stops, err)
		}
		owner := index.ResolveVehicleAttribution(server.ServerKey(), prefix+"R", prefix+"T")
		if !owner.Resolved() || owner.AgencyID != prefix+"A" {
			t.Fatalf("IDs no longer resolve to their upstream agency: %+v", owner)
		}
		if available, active := store.ScheduleStore().Evaluate(key, time.Date(2026, 9, 7, 8, 30, 0, 0, time.UTC)); !available || !active {
			t.Fatalf("merged schedule unavailable or inactive for %s", key)
		}
	}

	payload := marshalVehicleFeed(t, "north:101", 1, 2)
	var feed gtfsrt.FeedMessage
	if err := proto.Unmarshal(payload, &feed); err != nil {
		t.Fatal(err)
	}
	feed.Entity[0].Vehicle.Trip = &gtfsrt.TripDescriptor{TripId: proto.String("north:T"), RouteId: proto.String("north:R")}
	feed.Entity[0].Vehicle.StopId = proto.String("north:S")
	payload, err := proto.Marshal(&feed)
	if err != nil {
		t.Fatal(err)
	}
	rtServer := serveBytes(t, payload)
	defer rtServer.Close()
	server.GtfsRTFeeds = []models.GtfsRTFeed{{VehiclePositionURL: rtServer.URL}}
	if err := service.FetchAndStoreGTFSRTFeed(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	vehicle := realtime.Get(server.ServerKey()).Vehicles[0]
	if vehicle.Vehicle.ID.ID != "north:101" || vehicle.Vehicle.Trip.ID.ID != "north:T" || vehicle.Vehicle.Trip.ID.RouteID != "north:R" || vehicle.Vehicle.StopID == nil || *vehicle.Vehicle.StopID != "north:S" {
		t.Fatalf("realtime IDs changed: %+v", vehicle)
	}
}

func captureDuplicateEvents(t *testing.T) *[]*sentry.Event {
	t.Helper()
	var events []*sentry.Event
	previous := sentry.CurrentHub().Client()
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: "https://public@sentry.example.com/1",
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sentry.CurrentHub().BindClient(client)
	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })
	return &events
}

func TestMergedStaticDuplicatesAreReportedAndNeverPublished(t *testing.T) {
	for _, table := range []string{"agency.txt", "routes.txt", "stops.txt", "trips.txt", "calendar.txt"} {
		t.Run(table, func(t *testing.T) {
			events := captureDuplicateEvents(t)
			files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
			lines := strings.Split(strings.TrimSpace(files[table]), "\n")
			files[table] += lines[1] + "\n" // Even an identical row is an error.
			ts := serveBytes(t, makeScheduleZip(t, files))
			defer ts.Close()
			server := scheduleServer("A", ts.URL)
			store := NewStaticStore()
			previous := &models.StaticData{}
			store.Set(server.ServerKey(), previous)
			service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
			var failure StaticFeedRefreshObservation
			service.SetStaticFeedRefreshObserver(func(_ models.ObaServer, _ string, observation StaticFeedRefreshObservation) { failure = observation })
			service.DownloadGTFSBundles(context.Background(), []models.ObaServer{server}, 1)
			if current, _ := store.Get(server.ServerKey()); current != previous {
				t.Fatal("duplicate feed replaced the last good snapshot")
			}
			if failure.Success || failure.Reason != StaticReasonDuplicateID || failure.Stage != StaticFailureParse {
				t.Fatalf("missing duplicate failure observation: %+v", failure)
			}
			if len(*events) == 0 {
				t.Fatal("duplicate feed did not produce a Sentry event")
			}
		})
	}
}

func TestMergedRealtimeDuplicatesAreReportedAndNeverPublished(t *testing.T) {
	for _, kind := range []string{"entity_id", "vehicle_id"} {
		t.Run(kind, func(t *testing.T) {
			events := captureDuplicateEvents(t)
			var feed gtfsrt.FeedMessage
			if err := proto.Unmarshal(marshalVehicleFeed(t, "north:101", 1, 2), &feed); err != nil {
				t.Fatal(err)
			}
			other := proto.Clone(feed.Entity[0]).(*gtfsrt.FeedEntity)
			if kind == "entity_id" {
				other.Vehicle.Vehicle.Id = proto.String("south:101")
			} else {
				other.Id = proto.String("second")
			}
			feed.Entity = append(feed.Entity, other)
			data, err := proto.Marshal(&feed)
			if err != nil {
				t.Fatal(err)
			}
			ts := serveBytes(t, data)
			defer ts.Close()
			server := models.ObaServer{ServerName: "duplicates", ObaBaseURL: ts.URL, GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: ts.URL}}}
			store := NewRealtimeStore()
			previous := &models.RealtimeData{}
			store.Set(server.ServerKey(), previous)
			var observation FeedObservation
			store.SetObserver(func(_ models.ObaServer, result FeedObservation) { observation = result })
			err = fetchAndStoreGTFSRTFeed(context.Background(), server, store, ts.Client(), nil)
			var duplicate *duplicateIDError
			if !errors.As(err, &duplicate) || duplicate.kind != kind {
				t.Fatalf("expected %s rejection, got %v", kind, err)
			}
			if store.Get(server.ServerKey()) != previous {
				t.Fatal("duplicate feed replaced the last good snapshot")
			}
			if observation.Success || !observation.DuplicateID {
				t.Fatalf("missing duplicate failure observation: %+v", observation)
			}
			if len(*events) == 0 {
				t.Fatal("duplicate feed did not produce a Sentry event")
			}
		})
	}
}

func TestStaticRefreshCampaignRejectsRepeatedDuplicateArtifact(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["stops.txt"] += "S1,Conflicting stop,3,4\n"
	ts := serveBytes(t, makeScheduleZip(t, files))
	defer ts.Close()
	server := scheduleServer("A", ts.URL)
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	previous := &models.StaticData{}
	store.Set(server.ServerKey(), previous)
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(), slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	var failure StaticFeedRefreshObservation
	service.SetStaticFeedRefreshObserver(func(_ models.ObaServer, _ string, result StaticFeedRefreshObservation) { failure = result })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service.runStaticRefreshCampaign(ctx, server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Second})
	if ctx.Err() != nil {
		t.Fatal("campaign did not finish rejecting the duplicate artifact")
	}
	if current, _ := store.Get(server.ServerKey()); current != previous {
		t.Fatal("campaign replaced the last good snapshot")
	}
	if failure.Success || failure.Reason != StaticReasonDuplicateID || !failure.Conservative {
		t.Fatalf("missing bounded duplicate failure and conservative retry state: %+v", failure)
	}
	if !service.refreshCoordinator.isConservative(server, ts.URL) {
		t.Fatal("identical duplicate artifacts did not enter conservative retry mode")
	}
}

func TestMergedStaticDuplicateTableIsRejected(t *testing.T) {
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for i := 0; i < 2; i++ {
		file, err := archive.Create("stops.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,2\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := parseStaticBundleData(data.Bytes(), "https://merged.example/static.zip", "A")
	var failure *StaticFeedError
	if !errors.As(err, &failure) || failure.Reason != StaticReasonDuplicateID {
		t.Fatalf("duplicate table was not reported as a feed problem: %v", err)
	}
}
