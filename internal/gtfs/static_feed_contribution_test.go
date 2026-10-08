package gtfs

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestReduceStaticFeedAgencyModeKeepsOnlyOwnedData(t *testing.T) {
	zipData := readFixture(t, "gtfs.zip")
	feedURL := "https://feeds.example/agency.zip"
	server := models.ObaServer{
		ServerName: "agency", AgencyID: "40", AgencyName: "Sound Transit",
		ObaBaseURL: "https://oba.example", GtfsStaticFeeds: []string{feedURL},
	}
	bundle, err := parseStaticBundleData(zipData, feedURL, server.AgencyID)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	wantStatic := buildAgencyStaticSnapshot(server, []*remoteGtfs.Static{bundle}, nil)
	wantBounds := computeBoundingBoxes([]*remoteGtfs.Static{bundle}, server.AgencyID)
	wantMappings := classifyStaticFeedMappings(server, []downloadedStaticFeed{{url: feedURL, bundle: bundle}})
	wantSchedules, err := compileSchedules(server, []downloadedStaticFeed{{url: feedURL, data: zipData}})
	if err != nil {
		t.Fatalf("compile expected schedule: %v", err)
	}
	contribution, err := reduceStaticFeed(server, feedURL, zipData, bundle, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reduce feed: %v", err)
	}

	if !reflect.DeepEqual(contribution.data, wantStatic.data) {
		t.Fatalf("reduced static data differs from agency snapshot:\n got: %+v\nwant: %+v", contribution.data, wantStatic.data)
	}
	if !reflect.DeepEqual(contribution.routeIDs, wantStatic.routeIDs) || !reflect.DeepEqual(contribution.tripIDs, wantStatic.tripIDs) {
		t.Fatalf("reduced attribution differs: routes=%v trips=%v; want routes=%v trips=%v", contribution.routeIDs, contribution.tripIDs, wantStatic.routeIDs, wantStatic.tripIDs)
	}
	if !reflect.DeepEqual(contribution.agencyNames, wantStatic.agencyNames) {
		t.Fatalf("agency names = %v, want %v", contribution.agencyNames, wantStatic.agencyNames)
	}
	if !reflect.DeepEqual(contribution.boundingBoxes, wantBounds) {
		t.Fatalf("bounding-box contribution = %+v, want %+v", contribution.boundingBoxes, wantBounds)
	}
	if !reflect.DeepEqual(contribution.agencyMapping, wantMappings.Results[0]) {
		t.Fatalf("feed mapping = %+v, want %+v", contribution.agencyMapping, wantMappings.Results[0])
	}
	if !reflect.DeepEqual(contribution.scheduleSnapshots, wantSchedules) {
		t.Fatalf("schedule contribution = %+v, want %+v", contribution.scheduleSnapshots, wantSchedules)
	}
	if contribution.url != feedURL || contribution.contentHash == "" {
		t.Fatalf("feed identity/hash not retained: %+v", contribution)
	}

	// The contribution must own its static values. Mutating the parser graph
	// after reduction must not change the data that a later campaign phase uses.
	originalStopName := contribution.data.Stops[0].Name
	originalRouteName := contribution.data.Routes[0].LongName
	originalAgencyName := contribution.data.Agencies[0].Name
	bundle.Stops[0].Name = "changed in parser graph"
	bundle.Routes[0].LongName = "changed in parser graph"
	bundle.Agencies[0].Name = "changed in parser graph"
	if contribution.data.Stops[0].Name != originalStopName || contribution.data.Routes[0].LongName != originalRouteName || contribution.data.Agencies[0].Name != originalAgencyName {
		t.Fatal("reduced agency contribution still refers to parser graph values")
	}
	for i := range contribution.data.Routes {
		if contribution.data.Routes[i].Agency != &contribution.data.Agencies[0] {
			t.Fatal("retained route does not point to the contribution's agency")
		}
	}
}

func TestReduceStaticFeedServerModeKeepsMergedDataAndOwnership(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,UTC\nB,Agency B,https://b.example,UTC\n"
	files["routes.txt"] = "route_id,agency_id,route_type\nRA,A,3\nRB,B,3\n"
	files["trips.txt"] = "route_id,service_id,trip_id\nRA,WK,TA\nRB,WK,TB\n"
	files["stops.txt"] = "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\nSTATION,Station,47.6,-122.3,1,\nSA,Stop A,47.61,-122.31,0,STATION\nSB,Stop B,47.62,-122.32,0,STATION\n"
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nTA,08:00:00,08:00:00,SA,1\nTA,08:10:00,08:10:00,SA,2\nTB,08:00:00,08:00:00,SB,1\nTB,08:10:00,08:10:00,SB,2\n"
	zipData := makeScheduleZip(t, files)
	feedURL := "https://feeds.example/server.zip"
	server := models.ObaServer{ServerName: "server", ObaBaseURL: "https://oba.example", GtfsStaticFeeds: []string{feedURL}}
	bundle, err := parseStaticBundleData(zipData, feedURL, "")
	if err != nil {
		t.Fatalf("parse feed: %v", err)
	}

	wantStatic, declared := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundle})
	wantRoutes, wantTrips := buildAttributionMaps(server, []*remoteGtfs.Static{bundle}, false, nil)
	wantBounds := computeBoundingBoxes([]*remoteGtfs.Static{bundle}, "")
	wantMappings := classifyStaticFeedMappings(server, []downloadedStaticFeed{{url: feedURL, bundle: bundle}})
	wantSchedules, err := compileSchedules(server, []downloadedStaticFeed{{url: feedURL, data: zipData}})
	if err != nil {
		t.Fatalf("compile expected schedule: %v", err)
	}
	contribution, err := reduceStaticFeed(server, feedURL, zipData, bundle, nil)
	if err != nil {
		t.Fatalf("reduce feed: %v", err)
	}

	if !reflect.DeepEqual(contribution.data, cloneStaticData(wantStatic)) {
		t.Fatalf("reduced merged data differs:\n got: %+v\nwant: %+v", contribution.data, cloneStaticData(wantStatic))
	}
	if !reflect.DeepEqual(contribution.routeIDs, wantRoutes) || !reflect.DeepEqual(contribution.tripIDs, wantTrips) {
		t.Fatalf("attribution maps differ: routes=%v trips=%v; want routes=%v trips=%v", contribution.routeIDs, contribution.tripIDs, wantRoutes, wantTrips)
	}
	if !reflect.DeepEqual(contribution.boundingBoxes, wantBounds) {
		t.Fatalf("bounding-box contribution = %+v, want %+v", contribution.boundingBoxes, wantBounds)
	}
	if !reflect.DeepEqual(contribution.agencyMapping, wantMappings.Results[0]) {
		t.Fatalf("feed mapping = %+v, want %+v", contribution.agencyMapping, wantMappings.Results[0])
	}
	if len(contribution.agencyNames) != len(declared) {
		t.Fatalf("agency names = %v, want one for each declared agency: %v", contribution.agencyNames, declared)
	}
	for _, agency := range declared {
		if contribution.agencyNames[agency.AgencyID] != agency.AgencyName {
			t.Errorf("agency %q name = %q, want %q", agency.AgencyID, contribution.agencyNames[agency.AgencyID], agency.AgencyName)
		}
	}
	if !reflect.DeepEqual(contribution.scheduleSnapshots, wantSchedules) {
		t.Fatalf("schedule contribution = %+v, want %+v", contribution.scheduleSnapshots, wantSchedules)
	}

	for i := range contribution.data.Routes {
		agency := contribution.data.Routes[i].Agency
		if agency == nil || agency == &bundle.Agencies[0] || agency == &bundle.Agencies[1] {
			t.Fatalf("route %q retains an external agency pointer: %p", contribution.data.Routes[i].Id, agency)
		}
	}
	for i := range contribution.data.Stops {
		if parent := contribution.data.Stops[i].Parent; parent != nil {
			if parent == &bundle.Stops[0] || parent == &bundle.Stops[1] || parent == &bundle.Stops[2] {
				t.Fatalf("stop %q retains an external parent pointer", contribution.data.Stops[i].Id)
			}
		}
	}

	bundle.Agencies[0].Name = "changed in parser graph"
	bundle.Stops[0].Name = "changed in parser graph"
	if contribution.data.Agencies[0].Name == "changed in parser graph" || contribution.data.Stops[0].Name == "changed in parser graph" {
		t.Fatal("server contribution still refers to parser graph values")
	}
}

func TestReduceStaticFeedRejectsInvalidSchedule(t *testing.T) {
	files := basicScheduleFiles("Not/A-Timezone", "08:00:00", "09:00:00")
	zipData := makeScheduleZip(t, files)
	feedURL := "https://feeds.example/invalid-schedule.zip"
	server := models.ObaServer{AgencyID: "A", ObaBaseURL: "https://oba.example", GtfsStaticFeeds: []string{feedURL}}
	bundle, err := parseStaticBundleData(zipData, feedURL, server.AgencyID)
	if err != nil {
		t.Fatalf("parse static tables: %v", err)
	}

	contribution, err := reduceStaticFeed(server, feedURL, zipData, bundle, nil)
	if err == nil || contribution != nil {
		t.Fatalf("invalid schedule produced a contribution: contribution=%+v err=%v", contribution, err)
	}
	failure := asStaticFeedError(err)
	if failure.Stage != StaticFailureSchedule || failure.Reason != StaticReasonInvalidSchedule || failure.FeedURL != feedURL {
		t.Fatalf("unexpected schedule failure classification: %+v", failure)
	}
}
