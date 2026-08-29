package metrics

import (
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// serverModeFixture builds a server-scoped entry serving two agencies, a
// route → agency index covering both, and a merged GTFS-RT feed holding one
// vehicle per agency plus one vehicle on an unknown route.
func serverModeFixture(t *testing.T, baseURL string) (models.ObaServer, []models.ObaServer, *gtfs.RealtimeStore, *gtfs.RouteAgencyIndex) {
	t.Helper()

	server := models.ObaServer{ServerName: "multi", ObaBaseURL: baseURL}
	agencies := []models.ObaServer{
		{ServerName: "multi", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"},
		{ServerName: "multi", ObaBaseURL: baseURL, AgencyID: "agency-b", AgencyName: "Agency B"},
	}

	index := gtfs.NewRouteAgencyIndex()
	index.Set(baseURL, map[string]string{"route-a": "agency-a", "route-b": "agency-b"})

	now := time.Now().UTC()
	vehicle := func(id, routeID string, lat, lon float32) models.RealtimeVehicle {
		return models.RealtimeVehicle{
			FeedID: "0",
			Vehicle: remoteGtfs.Vehicle{
				ID:        &remoteGtfs.VehicleID{ID: id},
				Trip:      &remoteGtfs.Trip{ID: remoteGtfs.TripID{RouteID: routeID}},
				Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
				Timestamp: &now,
			},
		}
	}

	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{
		vehicle("va", "route-a", 47.60, -122.30),
		vehicle("vb", "route-b", 47.61, -122.31),
		vehicle("vx", "route-unknown", 47.62, -122.32),
	}})

	return server, agencies, store, index
}

// TestTrackVehicleTelemetryCountsEachVehicleOnceInServerMode pins the core
// server-mode contract: the telemetry pass runs once per server per tick, so
// each vehicle's report counter advances exactly once even though the server
// serves several agencies.
func TestTrackVehicleTelemetryCountsEachVehicleOnceInServerMode(t *testing.T) {
	const baseURL = "https://once.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getCounterValue(VehicleReportCount, map[string]string{
		"vehicle_id":  "va",
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  baseURL,
		"feed":        "0",
	})
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected vehicle va to be counted once per tick, got %v", got)
	}
}

// TestTrackVehicleTelemetryKeysLastSeenByAttributedAgency ensures each vehicle
// lands in the last-seen slot of the agency that actually owns its route,
// rather than under every agency on the server.
func TestTrackVehicleTelemetryKeysLastSeenByAttributedAgency(t *testing.T) {
	const baseURL = "https://attributed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	for _, tc := range []struct{ agencyID, vehicleID string }{
		{"agency-a", "va"},
		{"agency-b", "vb"},
	} {
		key := models.ServerKey(baseURL, tc.agencyID)
		if got := lastSeen.Count(key); got != 1 {
			t.Fatalf("expected 1 tracked vehicle for %s, got %d", tc.agencyID, got)
		}
		if _, ok := lastSeen.Get(key, "0", tc.vehicleID); !ok {
			t.Fatalf("expected %s to be tracked under %s", tc.vehicleID, tc.agencyID)
		}
	}
}

// TestCountVehiclePositionsBucketsByAgencyInServerMode checks that the
// position gauge reports each agency's own vehicle count instead of the
// server-wide total repeated under every agency's labels.
func TestCountVehiclePositionsBucketsByAgencyInServerMode(t *testing.T) {
	const baseURL = "https://buckets.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	total, err := countVehiclePositions(server, agencies, store, index)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected the server-wide total to be 3, got %d", total)
	}

	for _, agency := range agencies {
		got, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
			"agency_id":   agency.AgencyID,
			"agency_name": agency.AgencyName,
			"server_name": agency.ServerName,
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", agency.AgencyID, err)
		}
		if got != 1 {
			t.Fatalf("expected %s to report 1 vehicle, got %v", agency.AgencyID, got)
		}
	}
}

// TestCountVehiclePositionsZeroesAgenciesWithNoVehicles guards against a
// stuck series: an agency that had vehicles last tick and none this tick must
// report 0 rather than keeping its previous value.
func TestCountVehiclePositionsZeroesAgenciesWithNoVehicles(t *testing.T) {
	const baseURL = "https://zeroed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	if _, err := countVehiclePositions(server, agencies, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Agency B's vehicle disappears from the feed.
	data := store.Get(server.ServerKey())
	data.Vehicles = data.Vehicles[:1]
	store.Set(server.ServerKey(), data)

	if _, err := countVehiclePositions(server, agencies, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	got, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   "agency-b",
		"agency_name": "Agency B",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected agency-b to fall back to 0, got %v", got)
	}
}

// TestTrackInvalidVehiclesBucketsByAgencyInServerMode checks the invalid /
// out-of-bounds gauges are attributed per agency rather than reporting the
// server-wide count under each agency's labels.
func TestTrackInvalidVehiclesBucketsByAgencyInServerMode(t *testing.T) {
	const baseURL = "https://invalid.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	// Agency A's vehicle loses its position; agency B's stays valid.
	data := store.Get(server.ServerKey())
	data.Vehicles[0].Vehicle.Position = nil
	store.Set(server.ServerKey(), data)

	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Assert the series exist before reading their values: getMetricValue
	// materializes a missing series at 0, so a bare "expected 0" below would
	// pass even if the pass had emitted nothing at all.
	if got := len(gaugeSeriesForServer(t, InvalidVehicleCoordinatesGauge, baseURL)); got != 3 {
		t.Fatalf("expected 2 agency series plus the server-scoped catch-all, got %d", got)
	}

	for _, tc := range []struct {
		agencyID, agencyName string
		want                 float64
	}{
		{"agency-a", "Agency A", 1},
		{"agency-b", "Agency B", 0},
	} {
		got, err := getMetricValue(InvalidVehicleCoordinatesGauge, map[string]string{
			"agency_id":   tc.agencyID,
			"agency_name": tc.agencyName,
			"server_name": "multi",
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", tc.agencyID, err)
		}
		if got != tc.want {
			t.Fatalf("expected %s to report %v invalid vehicles, got %v", tc.agencyID, tc.want, got)
		}
	}
}

// TestTrackVehicleTelemetryAgencyModeIgnoresRouteIndex is a regression test
// for the dispatch bug: agency-mode must attribute every vehicle to the
// configured agency, even when the route → agency index is empty because the
// static bundle failed to download.
func TestTrackVehicleTelemetryAgencyModeIgnoresRouteIndex(t *testing.T) {
	const baseURL = "https://agencymode.example.com"
	server := models.ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"}

	now := time.Now().UTC()
	lat, lon := float32(47.60), float32(-122.30)
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{{
		FeedID: "0",
		Vehicle: remoteGtfs.Vehicle{
			ID:        &remoteGtfs.VehicleID{ID: "va"},
			Trip:      &remoteGtfs.Trip{ID: remoteGtfs.TripID{RouteID: "route-a"}},
			Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
			Timestamp: &now,
		},
	}}})

	lastSeen := NewVehicleLastSeen()
	// Empty index: no route is resolvable.
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	if got := lastSeen.Count(server.ServerKey()); got != 1 {
		t.Fatalf("expected agency-mode to track the vehicle regardless of the route index, got %d", got)
	}
}

// TestTrackVehicleTelemetryZeroesTrackedVehiclesOnEmptyFeed pins a sharp
// signal: when the feed reports nothing at all, the tracked gauge drops to 0
// immediately rather than coasting on last-seen entries that ClearRoutine will
// not expire for another hour.
func TestTrackVehicleTelemetryZeroesTrackedVehiclesOnEmptyFeed(t *testing.T) {
	const baseURL = "https://emptyfeed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// The feed goes empty, but the last-seen entries are still well inside
	// the staleness threshold.
	store.Set(server.ServerKey(), &models.RealtimeData{})
	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	for _, agency := range agencies {
		got, err := getMetricValue(TrackedVehiclesGauge, map[string]string{
			"agency_id":   agency.AgencyID,
			"agency_name": agency.AgencyName,
			"server_name": agency.ServerName,
			"server_url":  baseURL,
		})
		if err != nil {
			t.Fatalf("read gauge for %s: %v", agency.AgencyID, err)
		}
		if got != 0 {
			t.Fatalf("expected %s to report 0 tracked vehicles on an empty feed, got %v", agency.AgencyID, got)
		}
	}
}

// ghostVehicle is the worst-shaped entity a GTFS-RT feed can carry: no
// TripDescriptor (so there is no route_id to attribute it with) and no
// position at all. It is precisely what
// gtfs_rt_invalid_vehicle_coordinates exists to catch, so no amount of
// attribution logic may make it disappear.
func ghostVehicle(id string) models.RealtimeVehicle {
	return models.RealtimeVehicle{
		FeedID:  "0",
		Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: id}},
	}
}

// wideOpenBounds returns a bounding box store containing the whole planet for
// the given server key, so bounding-box checks never fire.
func wideOpenBounds(server models.ObaServer) *geo.BoundingBoxStore {
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})
	return bounds
}

// gaugeSeriesForServer returns the values of every series a gauge currently
// exposes for the given server_url. Unlike getMetricValue it never creates a
// series as a side effect, so it can prove a code path emitted nothing.
// gaugeSeriesForServer returns the values of every series a gauge exposes for
// one server_url, without materializing any.
func gaugeSeriesForServer(t *testing.T, vec *prometheus.GaugeVec, serverURL string) []float64 {
	t.Helper()

	matched := seriesMatching(vec, map[string]string{"server_url": serverURL})
	values := make([]float64, 0, len(matched))
	for _, pb := range matched {
		values = append(values, pb.GetGauge().GetValue())
	}
	return values
}

func sumFloats(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

// TestTrackInvalidVehiclesCountsUnattributableVehiclesInServerMode is the
// regression test for attribution swallowing malformed entities: a vehicle
// with no TripDescriptor and no position cannot be attributed to an agency,
// but it must still be counted — under the server-scoped entry's labels.
func TestTrackInvalidVehiclesCountsUnattributableVehiclesInServerMode(t *testing.T) {
	const baseURL = "https://ghost.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	data := store.Get(server.ServerKey())
	data.Vehicles = append(data.Vehicles, ghostVehicle("vghost"))
	store.Set(server.ServerKey(), data)

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, wideOpenBounds(server), store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getMetricValue(InvalidVehicleCoordinatesGauge, map[string]string{
		"agency_id":   "",
		"agency_name": "",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read server-scoped gauge: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected the unattributable malformed vehicle to be counted under the server-scoped series, got %v", got)
	}
}

// TestTrackInvalidVehiclesTotalsMatchServerWideCount pins the invariant an
// operator relies on: sum by (server_url) over the gauge equals the number of
// bad vehicles in the feed, whether or not each one could be attributed.
func TestTrackInvalidVehiclesTotalsMatchServerWideCount(t *testing.T) {
	const baseURL = "https://totals.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	data := store.Get(server.ServerKey())
	// One attributable vehicle loses its position, and one unattributable
	// entity is malformed from the start.
	data.Vehicles[0].Vehicle.Position = nil
	data.Vehicles = append(data.Vehicles, ghostVehicle("vghost"))
	store.Set(server.ServerKey(), data)

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, wideOpenBounds(server), store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	if got := sumFloats(gaugeSeriesForServer(t, InvalidVehicleCoordinatesGauge, baseURL)); got != 2 {
		t.Fatalf("expected the per-agency series to sum to the server-wide count of 2, got %v", got)
	}

	perAgency, err := getMetricValue(InvalidVehicleCoordinatesGauge, map[string]string{
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read agency-a gauge: %v", err)
	}
	if perAgency != 1 {
		t.Fatalf("expected agency-a to keep its own breakdown of 1, got %v", perAgency)
	}
}

// TestTrackInvalidVehiclesEmitsServerScopedZero guards against a stuck
// series: once the malformed entity leaves the feed, the server-scoped series
// must fall back to 0 instead of holding its previous value.
func TestTrackInvalidVehiclesEmitsServerScopedZero(t *testing.T) {
	const baseURL = "https://scopedzero.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	bounds := wideOpenBounds(server)

	data := store.Get(server.ServerKey())
	clean := append([]models.RealtimeVehicle(nil), data.Vehicles...)
	data.Vehicles = append(data.Vehicles, ghostVehicle("vghost"))
	store.Set(server.ServerKey(), data)

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	data.Vehicles = clean
	store.Set(server.ServerKey(), data)
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	// Two agencies plus the server-scoped catch-all: the catch-all has to be
	// on the wire before it can report 0, so count the series rather than
	// reading one (which would create it as a side effect).
	if series := gaugeSeriesForServer(t, InvalidVehicleCoordinatesGauge, baseURL); len(series) != 3 {
		t.Fatalf("expected 2 agency series plus a server-scoped one, got %d", len(series))
	}

	got, err := getMetricValue(InvalidVehicleCoordinatesGauge, map[string]string{
		"agency_id":   "",
		"agency_name": "",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read server-scoped gauge: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected the server-scoped series to fall back to 0, got %v", got)
	}
}

// TestTrackStoppedOutOfBoundsCountsUnattributableVehiclesInServerMode keeps
// the out-of-bounds gauge consistent with the invalid-coordinates gauge: a
// stopped vehicle outside the box counts even when its route is unknown.
func TestTrackStoppedOutOfBoundsCountsUnattributableVehiclesInServerMode(t *testing.T) {
	const baseURL = "https://oob.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	stopped := remoteGtfs.CurrentStatus(VehicleStatusStoppedAtStop)
	data := store.Get(server.ServerKey())
	// data.Vehicles[2] rides route-unknown, so it cannot be attributed.
	data.Vehicles[2].Vehicle.CurrentStatus = &stopped
	store.Set(server.ServerKey(), data)

	// A box that excludes every vehicle in the fixture.
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: 0, MaxLat: 1, MinLon: 0, MaxLon: 1})

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getMetricValue(StoppedOutOfBoundsVehiclesGauge, map[string]string{
		"agency_id":   "",
		"agency_name": "",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read server-scoped gauge: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected the unattributable stopped vehicle to be counted under the server-scoped series, got %v", got)
	}
}

func TestTrackStoppedOutOfBoundsUsesAttributedAgencyBoundingBox(t *testing.T) {
	const baseURL = "https://agencybounds.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)

	stopped := remoteGtfs.CurrentStatus(VehicleStatusStoppedAtStop)
	data := store.Get(server.ServerKey())
	data.Vehicles[0].Vehicle.CurrentStatus = &stopped // agency-a
	store.Set(server.ServerKey(), data)

	bounds := geo.NewBoundingBoxStore()
	// Agency A's vehicle is outside this box, but inside the server-wide box.
	bounds.Set(models.ServerKey(baseURL, "agency-a"), geo.BoundingBox{MinLat: 0, MaxLat: 1, MinLon: 0, MaxLon: 1})
	bounds.Set(models.ServerKey(baseURL, "agency-b"), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, agencies, bounds, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getMetricValue(StoppedOutOfBoundsVehiclesGauge, map[string]string{
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read agency-a gauge: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected agency-a vehicle to be checked against its own bounding box, got %v", got)
	}

	serverGot, err := getMetricValue(StoppedOutOfBoundsVehiclesGauge, map[string]string{
		"agency_id":   "",
		"agency_name": "",
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read server-scoped gauge: %v", err)
	}
	if serverGot != 0 {
		t.Fatalf("expected attributed vehicle not to be counted by server-scoped fallback, got %v", serverGot)
	}
}

// TestTrackInvalidVehiclesAgencyModeEmitsSingleSeries pins that agency-mode
// is untouched: every vehicle belongs to the configured entry, so a malformed
// one is counted there and no server-scoped series appears alongside it.
func TestTrackInvalidVehiclesAgencyModeEmitsSingleSeries(t *testing.T) {
	const baseURL = "https://agencyghost.example.com"
	server := models.ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"}

	now := time.Now().UTC()
	lat, lon := float32(47.60), float32(-122.30)
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{
		{FeedID: "0", Vehicle: remoteGtfs.Vehicle{
			ID:        &remoteGtfs.VehicleID{ID: "va"},
			Trip:      &remoteGtfs.Trip{ID: remoteGtfs.TripID{RouteID: "route-a"}},
			Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
			Timestamp: &now,
		}},
		ghostVehicle("vghost"),
	}})

	// Empty index: agency-mode must not consult it at all.
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, nil, wideOpenBounds(server), store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	series := gaugeSeriesForServer(t, InvalidVehicleCoordinatesGauge, baseURL)
	if len(series) != 1 {
		t.Fatalf("expected agency-mode to emit exactly one series, got %d", len(series))
	}
	if series[0] != 1 {
		t.Fatalf("expected the configured agency to report 1 invalid vehicle, got %v", series[0])
	}
}

// TestTrackVehicleTelemetryCountsNilIDVehicleAsUnattributed closes the hole
// where a vehicle with no ID was skipped before it could be counted anywhere:
// it appears in no per-agency series, so it must show up in the unattributed
// gauge.
func TestTrackVehicleTelemetryCountsNilIDVehicleAsUnattributed(t *testing.T) {
	const baseURL = "https://nilid.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	data := store.Get(server.ServerKey())
	data.Vehicles = append(data.Vehicles, models.RealtimeVehicle{FeedID: "0"})
	store.Set(server.ServerKey(), data)

	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getMetricValue(GtfsRtUnattributedVehicles, map[string]string{
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	// The fixture's route-unknown vehicle plus the ID-less one.
	if got != 2 {
		t.Fatalf("expected the ID-less vehicle to be counted as unattributed, got %v", got)
	}
}

// TestTrackVehicleTelemetryAgencyModeEmitsNoUnattributedSeries keeps the
// unattributed gauge a server-mode-only signal: in agency-mode every vehicle
// belongs to the configured agency by definition, ID or not.
func TestTrackVehicleTelemetryAgencyModeEmitsNoUnattributedSeries(t *testing.T) {
	const baseURL = "https://nilidagency.example.com"
	server := models.ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"}

	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{Vehicles: []models.RealtimeVehicle{{FeedID: "0"}}})

	if err := trackVehicleTelemetry(server, nil, NewVehicleLastSeen(), store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	if series := gaugeSeriesForServer(t, GtfsRtUnattributedVehicles, baseURL); len(series) != 0 {
		t.Fatalf("expected agency-mode to emit no unattributed series, got %d", len(series))
	}
}

// TestTrackVehicleTelemetryZeroesUnattributedVehiclesOnEmptyFeed is the
// regression test for the empty-feed early return skipping the unattributed
// gauge: every other vehicle metric drops to 0 when the feed goes empty, so a
// gauge frozen at its last non-zero count reads as a static-feed coverage
// problem that no longer exists — and nothing would ever clear it.
func TestTrackVehicleTelemetryZeroesUnattributedVehiclesOnEmptyFeed(t *testing.T) {
	const baseURL = "https://emptyunattributed.example.com"
	server, agencies, store, index := serverModeFixture(t, baseURL)
	lastSeen := NewVehicleLastSeen()

	// The fixture's route-unknown vehicle makes the first tick non-zero.
	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	before, err := getMetricValue(GtfsRtUnattributedVehicles, map[string]string{
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read gauge after first pass: %v", err)
	}
	if before == 0 {
		t.Fatalf("fixture should report unattributed vehicles on the first pass, got %v", before)
	}

	store.Set(server.ServerKey(), &models.RealtimeData{})
	if err := trackVehicleTelemetry(server, agencies, lastSeen, store, index); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	got, err := getMetricValue(GtfsRtUnattributedVehicles, map[string]string{
		"server_name": "multi",
		"server_url":  baseURL,
	})
	if err != nil {
		t.Fatalf("read gauge after empty feed: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected an empty feed to zero the unattributed gauge, got %v", got)
	}
}

// TestTrackVehicleTelemetryAgencyModeEmitsNoUnattributedSeriesOnEmptyFeed
// keeps the empty-feed path consistent with the non-empty one: the gauge is
// server-scoped, so agency-mode must publish no series for it at all.
func TestTrackVehicleTelemetryAgencyModeEmitsNoUnattributedSeriesOnEmptyFeed(t *testing.T) {
	const baseURL = "https://emptyagencymode.example.com"
	server := models.ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a", AgencyName: "Agency A"}

	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{})

	if err := trackVehicleTelemetry(server, nil, NewVehicleLastSeen(), store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	if series := gaugeSeriesForServer(t, GtfsRtUnattributedVehicles, baseURL); len(series) != 0 {
		t.Fatalf("expected agency-mode to emit no unattributed series on an empty feed, got %d", len(series))
	}
}
