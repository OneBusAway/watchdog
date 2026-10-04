package gtfs

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"reflect"
	"testing"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func TestBuildAgencyStaticSnapshotSelectsAndOwnsGraph(t *testing.T) {
	agencies := []remoteGtfs.Agency{
		{Id: "A", Name: "Agency A"},
		{Id: "B", Name: "Agency B"},
	}
	routes := []remoteGtfs.Route{
		{Id: "route-a", Agency: &agencies[0]},
		{Id: "route-a-unused", Agency: &agencies[0]},
		{Id: "route-b", Agency: &agencies[1]},
	}
	services := []remoteGtfs.Service{
		{Id: "service-a"},
		{Id: "service-b"},
	}
	parent := remoteGtfs.Stop{Id: "station-a", Type: 1, Latitude: floatPtr(47.60), Longitude: floatPtr(-122.30)}
	stops := []remoteGtfs.Stop{
		{Id: "stop-a", Type: 0, Parent: &parent, Latitude: floatPtr(47.61), Longitude: floatPtr(-122.31)},
		parent,
		{Id: "stop-b", Type: 0, Latitude: floatPtr(40.60), Longitude: floatPtr(-73.30)},
		{Id: "shared", Type: 0, Latitude: floatPtr(47.62), Longitude: floatPtr(-122.32)},
	}
	bundle := &remoteGtfs.Static{Agencies: agencies, Routes: routes, Services: services, Stops: stops}
	bundle.Trips = []remoteGtfs.ScheduledTrip{
		{ID: "trip-a", Route: &bundle.Routes[0], Service: &bundle.Services[0], Stops: []*remoteGtfs.Stop{&bundle.Stops[0], &bundle.Stops[3]}},
		{ID: "trip-b", Route: &bundle.Routes[2], Service: &bundle.Services[1], Stops: []*remoteGtfs.Stop{&bundle.Stops[2], &bundle.Stops[3]}},
	}

	server := models.ObaServer{ServerName: "test", AgencyID: "A", AgencyName: "Configured A", ObaBaseURL: "https://example.com"}
	result := buildAgencyStaticSnapshot(server, []*remoteGtfs.Static{bundle}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := len(result.data.Routes); got != 2 {
		t.Fatalf("expected both A routes, including unused route, got %d", got)
	}
	routeAgency, routeOK := singletonCandidate(result.routeIDs["route-b"])
	tripAgency, tripOK := singletonCandidate(result.tripIDs["trip-b"])
	if !routeOK || routeAgency != "B" || !tripOK || tripAgency != "B" {
		t.Fatalf("expected foreign attribution maps to remain resolvable: routes=%v trips=%v", result.routeIDs, result.tripIDs)
	}
	if len(result.data.Services) != 1 || result.data.Services[0].Id != "service-a" {
		t.Fatalf("expected only A service, got %+v", result.data.Services)
	}
	if len(result.data.Stops) != 3 {
		t.Fatalf("expected A stop, shared stop, and parent station, got %d", len(result.data.Stops))
	}

	byID := make(map[string]*remoteGtfs.Stop, len(result.data.Stops))
	for i := range result.data.Stops {
		byID[result.data.Stops[i].Id] = &result.data.Stops[i]
		if result.data.Stops[i].Parent != nil && result.data.Stops[i].Parent == &bundle.Stops[1] {
			t.Fatal("scoped stop retained a pointer into the source graph")
		}
	}
	if byID["stop-a"].Parent != byID["station-a"] || byID["stop-a"].Root() != byID["station-a"] {
		t.Fatal("expected stop parent chain to point inside scoped snapshot")
	}
	if result.data.Routes[0].Agency != &result.data.Agencies[0] {
		t.Fatal("expected retained route to point at scoped agency record")
	}
	if result.data.Routes[0].Agency == bundle.Routes[0].Agency {
		t.Fatal("retained route still points into source agency data")
	}
}

func TestBuildAgencyStaticSnapshotNormalizesSoleBlankAgency(t *testing.T) {
	bundle := &remoteGtfs.Static{
		Agencies: []remoteGtfs.Agency{{Name: "Unnamed ID Agency"}},
		Routes:   []remoteGtfs.Route{{Id: "route-a"}},
	}
	server := models.ObaServer{AgencyID: "configured", AgencyName: "Configured", ObaBaseURL: "https://example.com"}
	result := buildAgencyStaticSnapshot(server, []*remoteGtfs.Static{bundle}, nil)
	if len(result.data.Agencies) != 1 || result.data.Agencies[0].Id != "configured" {
		t.Fatalf("expected blank sole agency to be normalized, got %+v", result.data.Agencies)
	}
	if agencyID, ok := singletonCandidate(result.routeIDs["route-a"]); !ok || agencyID != "configured" {
		t.Fatalf("expected unqualified sole-agency route to use configured agency, got %v", result.routeIDs)
	}
}

func TestNormalizeOmittedAgencyIDAfterParsing(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.Create("agency.txt")
	if err != nil {
		t.Fatalf("create agency.txt: %v", err)
	}
	if _, err := file.Write([]byte("agency_name,agency_url,agency_timezone\nSingle Agency,https://example.com,UTC\n")); err != nil {
		t.Fatalf("write agency.txt: %v", err)
	}
	for name, content := range map[string]string{
		"routes.txt":     "route_id,route_type\nroute-a,3\n",
		"stops.txt":      "stop_id,stop_lat,stop_lon\nstop-a,1,2\n",
		"trips.txt":      "route_id,service_id,trip_id\nroute-a,service-a,trip-a\n",
		"stop_times.txt": "trip_id,stop_id,stop_sequence\ntrip-a,stop-a,1\n",
	} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	bundle, err := remoteGtfs.ParseStatic(data.Bytes(), remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatalf("parse static bundle: %v", err)
	}
	if bundle.Agencies[0].Id == "" {
		t.Fatal("expected go-gtfs to synthesize an ID before normalization")
	}
	if err := normalizeOmittedAgencyID(data.Bytes(), bundle); err != nil {
		t.Fatalf("normalize omitted agency ID: %v", err)
	}
	if bundle.Agencies[0].Id != "" {
		t.Fatalf("expected omitted agency ID to be restored as blank, got %q", bundle.Agencies[0].Id)
	}
	server := models.ObaServer{AgencyID: "configured", AgencyName: "Configured"}
	result := buildAgencyStaticSnapshot(server, []*remoteGtfs.Static{bundle}, nil)
	agencyID, ok := singletonCandidate(result.routeIDs["route-a"])
	if !ok || agencyID != server.AgencyID || len(result.data.Routes) != 1 {
		t.Fatalf("expected the normalized sole-agency route to belong to %q, got routes=%v map=%v", server.AgencyID, result.data.Routes, result.routeIDs)
	}
}

func TestBuildAttributionMapsRetainsCrossFeedCandidates(t *testing.T) {
	bundle := func(agencyID, routeID, tripID string) *remoteGtfs.Static {
		result := &remoteGtfs.Static{
			Agencies: []remoteGtfs.Agency{{Id: agencyID}},
			Routes:   []remoteGtfs.Route{{Id: routeID}},
		}
		result.Routes[0].Agency = &result.Agencies[0]
		result.Trips = []remoteGtfs.ScheduledTrip{{ID: tripID, Route: &result.Routes[0]}}
		return result
	}

	routes, trips := buildAttributionMaps(
		models.ObaServer{ServerName: "multi", ObaBaseURL: "https://example.com"},
		[]*remoteGtfs.Static{
			bundle("A", "shared-route", "trip-a"),
			bundle("B", "shared-route", "trip-b"),
			bundle("C", "shared-route", "trip-a"),
		},
		false,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	if got := routes["shared-route"]; !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("route candidates = %v", got)
	}
	if got := trips["trip-a"]; !reflect.DeepEqual(got, []string{"A", "C"}) {
		t.Fatalf("trip candidates = %v", got)
	}
	if got := trips["trip-b"]; !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("unique trip candidates = %v", got)
	}
}

// Agency mode keeps only the stops the configured agency's trips serve, read
// from the bundle as parseStaticBundleData produces it in production.
func TestAgencySnapshotFromParsedFeedKeepsServedStops(t *testing.T) {
	feed := makeScheduleZip(t, map[string]string{
		"agency.txt":     "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,UTC\nB,Agency B,https://b.example,UTC\n",
		"routes.txt":     "route_id,agency_id,route_type\nRA,A,3\nRB,B,3\n",
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,0,0,20260101,20261231\n",
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\nSTATION,Station,1,1,1,\nS1,One,1,1,0,STATION\nS2,Two,2,2,0,\nSB,B only,3,3,0,\n",
		"trips.txt":      "route_id,service_id,trip_id\nRA,WK,TA\nRB,WK,TB\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nTA,08:00:00,08:00:00,S1,1\nTA,08:10:00,08:10:00,S2,2\nTB,08:00:00,08:00:00,SB,1\n",
	})
	bundle, err := parseStaticBundleData(feed, "https://feed.zip", "A")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	result := buildAgencyStaticSnapshot(models.ObaServer{AgencyID: "A", AgencyName: "Agency A"}, []*remoteGtfs.Static{bundle}, nil)

	got := map[string]bool{}
	for _, stop := range result.data.Stops {
		got[stop.Id] = true
	}
	for _, want := range []string{"S1", "S2", "STATION"} {
		if !got[want] {
			t.Errorf("agency snapshot is missing served stop %s; has %v", want, got)
		}
	}
	if got["SB"] {
		t.Errorf("agency snapshot kept stop SB, which only agency B serves")
	}
}
