package gtfs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestBuildStaticSnapshotCandidateMatchesServerScopedBundle(t *testing.T) {
	feedAURL, feedBURL := "https://feeds.example/a.zip", "https://feeds.example/b.zip"
	server := models.ObaServer{
		ServerName: "server", ObaBaseURL: "https://oba.example",
		GtfsStaticFeeds: []string{feedAURL, feedBURL},
	}
	feedAData, feedA, bundleA := makeStaticFeedContributionFixture(t, server, feedAURL, "A", "Agency A", "route-A", "trip-A", "stop-A", "47.60", "-122.30", "UTC")
	feedBData, feedB, bundleB := makeStaticFeedContributionFixture(t, server, feedBURL, "B", "Agency B", "route-B", "trip-B", "stop-B", "40.60", "-73.30", "UTC")
	contributions := map[string]*staticFeedContribution{feedAURL: feedA, feedBURL: feedB}

	candidate, err := buildStaticSnapshotCandidate(server, contributions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("assemble contributions: %v", err)
	}
	bundles := []*remoteGtfs.Static{bundleA, bundleB}
	wantData, wantAgencies := mergeStaticAndDiscoverAgencies(bundles)
	wantRoutes, wantTrips := buildAttributionMaps(server, bundles, false, nil)
	wantBounds := computeBoundingBoxes(bundles, "")
	wantSchedules, err := compileSchedules(server, []downloadedStaticFeed{
		{url: feedAURL, data: feedAData, bundle: bundleA},
		{url: feedBURL, data: feedBData, bundle: bundleB},
	})
	if err != nil {
		t.Fatalf("compile expected schedules: %v", err)
	}
	wantMappings := classifyStaticFeedMappings(server, []downloadedStaticFeed{
		{url: feedAURL, bundle: bundleA},
		{url: feedBURL, bundle: bundleB},
	})

	if !reflect.DeepEqual(candidate.data, cloneStaticData(wantData)) {
		t.Fatalf("candidate static data differs:\n got: %+v\nwant: %+v", candidate.data, cloneStaticData(wantData))
	}
	if !reflect.DeepEqual(candidate.declaredAgencies, wantAgencies) {
		t.Fatalf("declared agencies = %+v, want %+v", candidate.declaredAgencies, wantAgencies)
	}
	if !reflect.DeepEqual(candidate.routeIDs, wantRoutes) || !reflect.DeepEqual(candidate.tripIDs, wantTrips) {
		t.Fatalf("attribution maps differ: routes=%v trips=%v; want routes=%v trips=%v", candidate.routeIDs, candidate.tripIDs, wantRoutes, wantTrips)
	}
	if !reflect.DeepEqual(candidate.boundingBoxes, wantBounds) {
		t.Fatalf("candidate bounds = %+v, want %+v", candidate.boundingBoxes, wantBounds)
	}
	if !reflect.DeepEqual(candidate.schedules, wantSchedules) {
		t.Fatalf("candidate schedules = %+v, want %+v", candidate.schedules, wantSchedules)
	}
	if !reflect.DeepEqual(candidate.mappings, wantMappings) {
		t.Fatalf("candidate feed mappings = %+v, want %+v", candidate.mappings, wantMappings)
	}

	staticStore := NewStaticStore()
	staticStore.SetConfiguredServers([]models.ObaServer{server})
	boundsStore := geo.NewBoundingBoxStore()
	routeIndex := NewRouteAgencyIndex()
	service := NewGtfsService(staticStore, NewRealtimeStore(), boundsStore, routeIndex, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	var observedMappings StaticFeedMappingObservation
	service.SetFeedMappingObserver(func(_ models.ObaServer, observation StaticFeedMappingObservation) {
		observedMappings = observation
	})
	if err := service.assembleAndPublishStaticSnapshot(context.Background(), server, contributions); err != nil {
		t.Fatalf("assemble and publish server snapshot: %v", err)
	}
	var shared *models.StaticData
	for _, agency := range wantAgencies {
		key := models.ServerKey(server.ObaBaseURL, agency.AgencyID)
		stored, ok := staticStore.Get(key)
		if !ok || !reflect.DeepEqual(stored, candidate.data) {
			t.Fatalf("stored data for agency %s = %+v, want candidate snapshot", agency.AgencyID, stored)
		}
		if shared == nil {
			shared = stored
		} else if stored != shared {
			t.Fatal("server agencies do not share the merged static snapshot")
		}
		if got, ok := boundsStore.Get(key); !ok || got != wantBounds.byAgency[agency.AgencyID] {
			t.Errorf("agency %s bounds = %+v (present=%t), want %+v", agency.AgencyID, got, ok, wantBounds.byAgency[agency.AgencyID])
		}
		if available, _ := staticStore.ScheduleStore().Evaluate(key, time.Date(2026, time.June, 1, 8, 30, 0, 0, time.UTC)); !available {
			t.Errorf("agency %s schedule was not published as available", agency.AgencyID)
		}
	}
	if got, ok := boundsStore.Get(server.ServerKey()); !ok || got != wantBounds.union {
		t.Fatalf("server-wide bounds = %+v (present=%t), want %+v", got, ok, wantBounds.union)
	}
	if agencyID, ok := routeIndex.Get(server.ServerKey(), "route-A"); !ok || agencyID != "A" {
		t.Fatalf("route attribution = %q, %t; want agency A", agencyID, ok)
	}
	if !reflect.DeepEqual(observedMappings, wantMappings) {
		t.Fatalf("published feed mappings = %+v, want %+v", observedMappings, wantMappings)
	}
}

func TestBuildStaticSnapshotCandidateMatchesAgencyScopedBundle(t *testing.T) {
	feedAURL, feedBURL := "https://feeds.example/a.zip", "https://feeds.example/b.zip"
	server := models.ObaServer{
		ServerName: "agency", AgencyID: "A", AgencyName: "Configured A", ObaBaseURL: "https://oba.example",
		GtfsStaticFeeds: []string{feedAURL, feedBURL},
	}
	feedAData, feedA, bundleA := makeStaticFeedContributionFixture(t, server, feedAURL, "A", "Agency A", "route-A", "trip-A", "stop-A", "47.60", "-122.30", "UTC")
	feedBData, feedB, bundleB := makeStaticFeedContributionFixture(t, server, feedBURL, "A", "Agency A second feed", "route-B", "trip-B", "stop-B", "47.70", "-122.20", "UTC")
	contributions := map[string]*staticFeedContribution{feedAURL: feedA, feedBURL: feedB}

	candidate, err := buildStaticSnapshotCandidate(server, contributions, nil)
	if err != nil {
		t.Fatalf("assemble contributions: %v", err)
	}
	bundles := []*remoteGtfs.Static{bundleA, bundleB}
	wantStatic := buildAgencyStaticSnapshot(server, bundles, nil)
	wantBounds := computeBoundingBoxes(bundles, server.AgencyID)
	wantSchedules, err := compileSchedules(server, []downloadedStaticFeed{
		{url: feedAURL, data: feedAData, bundle: bundleA},
		{url: feedBURL, data: feedBData, bundle: bundleB},
	})
	if err != nil {
		t.Fatalf("compile expected schedules: %v", err)
	}
	wantMappings := classifyStaticFeedMappings(server, []downloadedStaticFeed{
		{url: feedAURL, bundle: bundleA},
		{url: feedBURL, bundle: bundleB},
	})

	if !reflect.DeepEqual(candidate.data, wantStatic.data) {
		t.Fatalf("candidate agency data differs:\n got: %+v\nwant: %+v", candidate.data, wantStatic.data)
	}
	if !reflect.DeepEqual(candidate.routeIDs, wantStatic.routeIDs) || !reflect.DeepEqual(candidate.tripIDs, wantStatic.tripIDs) {
		t.Fatalf("attribution maps differ: routes=%v trips=%v; want routes=%v trips=%v", candidate.routeIDs, candidate.tripIDs, wantStatic.routeIDs, wantStatic.tripIDs)
	}
	if !reflect.DeepEqual(candidate.boundingBoxes, wantBounds) {
		t.Fatalf("candidate bounds = %+v, want %+v", candidate.boundingBoxes, wantBounds)
	}
	if !reflect.DeepEqual(candidate.schedules, wantSchedules) {
		t.Fatalf("candidate schedules = %+v, want %+v", candidate.schedules, wantSchedules)
	}
	if !reflect.DeepEqual(candidate.mappings, wantMappings) {
		t.Fatalf("candidate feed mappings = %+v, want %+v", candidate.mappings, wantMappings)
	}
}

func TestBuildStaticSnapshotCandidateRejectsMixedAgencyTimezones(t *testing.T) {
	firstURL, secondURL := "https://feeds.example/first.zip", "https://feeds.example/second.zip"
	server := models.ObaServer{
		ServerName: "agency", AgencyID: "A", AgencyName: "Agency A", ObaBaseURL: "https://oba.example",
		GtfsStaticFeeds: []string{firstURL, secondURL},
	}
	_, first, _ := makeStaticFeedContributionFixture(t, server, firstURL, "A", "Agency A", "route-A", "trip-A", "stop-A", "47.60", "-122.30", "UTC")
	_, second, _ := makeStaticFeedContributionFixture(t, server, secondURL, "A", "Agency A", "route-B", "trip-B", "stop-B", "47.70", "-122.20", "America/New_York")

	if _, err := buildStaticSnapshotCandidate(server, map[string]*staticFeedContribution{firstURL: first, secondURL: second}, nil); err == nil {
		t.Fatal("mixed cross-feed timezones were accepted")
	} else {
		failure := asStaticFeedError(err)
		if failure.FeedURL != secondURL || failure.Stage != StaticFailureCompleteValidate || failure.Reason != StaticReasonInvalidSchedule {
			t.Fatalf("unexpected cross-feed validation failure: %+v", failure)
		}
	}
}

func TestFailedStaticCampaignPreservesLastCompleteSnapshot(t *testing.T) {
	zipData := readFixture(t, "gtfs.zip")
	var goodRequests int
	var failedRequests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.zip":
			goodRequests++
			_, _ = w.Write(zipData)
		default:
			failedRequests++
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer ts.Close()

	server := models.ObaServer{
		ServerName: "agency", AgencyID: "40", AgencyName: "Sound Transit", ObaBaseURL: ts.URL,
		GtfsStaticFeeds: []string{ts.URL + "/good.zip", ts.URL + "/failed.zip"},
	}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})
	previous := &models.StaticData{Routes: []remoteGtfs.Route{{Id: "previous-route"}}}
	previousFetched := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	store.Set(server.ServerKey(), previous)
	store.SetFetchTime(server.ServerKey(), previousFetched)
	schedule := &ScheduleSnapshot{
		timezone: time.UTC, timezoneName: "UTC", complete: true,
		services: []scheduleService{{weekdays: 0x7f, start: 20260101, end: 20261231, windows: []scheduleWindow{{start: 0, end: 24 * time.Hour}}}},
	}
	store.ScheduleStore().Replace(server, map[string]*ScheduleSnapshot{server.ServerKey(): schedule})
	previousBounds := geo.BoundingBox{MinLat: 1, MaxLat: 2, MinLon: 3, MaxLon: 4}
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), previousBounds)
	routeIndex := NewRouteAgencyIndex()
	routeIndex.ReplaceCandidates(server.ServerKey(), map[string][]string{"previous-route": {server.AgencyID}}, nil, map[string]string{server.AgencyID: server.AgencyName})
	service := NewGtfsService(store, NewRealtimeStore(), bounds, routeIndex, slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())

	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshStartup, 1, staticRefreshRetryPolicy{
		initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Nanosecond,
	})

	if got, ok := store.Get(server.ServerKey()); !ok || got != previous {
		t.Fatal("failed partial refresh replaced the last complete static snapshot")
	}
	if got, ok := store.GetFetchTime(server.ServerKey()); !ok || !got.Equal(previousFetched) {
		t.Fatalf("fetch time changed after failed partial refresh: %v", got)
	}
	if got, ok := bounds.Get(server.ServerKey()); !ok || got != previousBounds {
		t.Fatalf("bounding box changed after failed partial refresh: %+v", got)
	}
	if agencyID, ok := routeIndex.Get(server.ServerKey(), "previous-route"); !ok || agencyID != server.AgencyID {
		t.Fatalf("route attribution changed after failed partial refresh: %q, %t", agencyID, ok)
	}
	available, active := store.ScheduleStore().Evaluate(server.ServerKey(), time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC))
	if !available || !active || store.ScheduleStore().data[server.ServerKey()].snapshot != schedule {
		t.Fatalf("last complete schedule was not kept active: available=%t active=%t", available, active)
	}
	if goodRequests != 1 || failedRequests != 1 {
		t.Fatalf("feed requests: successful=%d failed=%d, want one each", goodRequests, failedRequests)
	}
}

func makeStaticFeedContributionFixture(t *testing.T, server models.ObaServer, feedURL, agencyID, agencyName, routeID, tripID, stopID, latitude, longitude, timezone string) ([]byte, *staticFeedContribution, *remoteGtfs.Static) {
	t.Helper()
	files := basicScheduleFiles(timezone, "08:00:00", "09:00:00")
	files["agency.txt"] = fmt.Sprintf("agency_id,agency_name,agency_url,agency_timezone\n%s,%s,https://%s.example,%s\n", agencyID, agencyName, agencyID, timezone)
	files["routes.txt"] = fmt.Sprintf("route_id,agency_id,route_type\n%s,%s,3\n", routeID, agencyID)
	files["trips.txt"] = fmt.Sprintf("route_id,service_id,trip_id\n%s,WK,%s\n", routeID, tripID)
	files["stops.txt"] = fmt.Sprintf("stop_id,stop_name,stop_lat,stop_lon\n%s,%s,%s,%s\n", stopID, stopID, latitude, longitude)
	files["stop_times.txt"] = fmt.Sprintf("trip_id,arrival_time,departure_time,stop_id,stop_sequence\n%s,08:00:00,08:00:00,%s,1\n", tripID, stopID)
	zipData := makeScheduleZip(t, files)
	bundle, err := parseStaticBundleData(zipData, feedURL, server.AgencyID)
	if err != nil {
		t.Fatalf("parse %s: %v", feedURL, err)
	}
	contribution, err := buildStaticFeedContribution(server, feedURL, zipData, bundle, nil)
	if err != nil {
		t.Fatalf("reduce %s: %v", feedURL, err)
	}
	return zipData, contribution, bundle
}
