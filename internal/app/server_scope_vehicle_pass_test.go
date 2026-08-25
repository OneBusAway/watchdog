package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

// routedVehicle names a vehicle and the route it is running, which is what the
// route -> agency index needs in order to attribute it.
type routedVehicle struct{ vehicleID, routeID string }

// buildRoutedVehicleFeedProtobuf builds a GTFS-RT feed whose vehicles carry
// trip descriptors, so server-mode attribution has something to resolve.
func buildRoutedVehicleFeedProtobuf(t *testing.T, vehicles []routedVehicle) []byte {
	t.Helper()

	entities := make([]*gtfsrt.FeedEntity, 0, len(vehicles))
	for i, v := range vehicles {
		entities = append(entities, &gtfsrt.FeedEntity{
			Id: proto.String(v.vehicleID),
			Vehicle: &gtfsrt.VehiclePosition{
				Vehicle:   &gtfsrt.VehicleDescriptor{Id: proto.String(v.vehicleID)},
				Trip:      &gtfsrt.TripDescriptor{RouteId: proto.String(v.routeID)},
				Position:  &gtfsrt.Position{Latitude: proto.Float32(float32(1 + i)), Longitude: proto.Float32(2.0)},
				Timestamp: proto.Uint64(uint64(time.Now().Unix())),
			},
		})
	}

	data, err := proto.Marshal(&gtfsrt.FeedMessage{
		Header: &gtfsrt.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Incrementality:      gtfsrt.FeedHeader_FULL_DATASET.Enum(),
			Timestamp:           proto.Uint64(uint64(time.Now().Unix())),
		},
		Entity: entities,
	})
	if err != nil {
		t.Fatalf("marshal GTFS-RT feed: %v", err)
	}
	return data
}

// newTwoAgencyServerScope stands up an Application and a stub OBA server for a
// server-scoped entry serving agency-a and agency-b, whose merged GTFS-RT feed
// carries one vehicle per agency.
func newTwoAgencyServerScope(t *testing.T) (*Application, models.ObaServer, config.Scope) {
	t.Helper()

	rtBody := buildRoutedVehicleFeedProtobuf(t, []routedVehicle{
		{vehicleID: "va", routeID: "route-a"},
		{vehicleID: "vb", routeID: "route-b"},
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":["agency-a","agency-b"]}}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(http.DefaultClient.CloseIdleConnections)

	app := newTestApplication(t)
	baseURL := ts.URL

	server := models.ObaServer{
		ServerName: "multi",
		ObaBaseURL: baseURL,
		ObaApiKey:  "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{
			VehiclePositionURL: baseURL + "/vehicles.pb",
		}},
	}

	wholeWorld := geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}
	for _, agencyID := range []string{"agency-a", "agency-b"} {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(key, wholeWorld)
	}
	// The vehicle pass runs once for the whole server, so it reads the
	// server-scoped bounding box rather than any single agency's.
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)

	app.GtfsService.RouteAgencyIndex.Set(baseURL, map[string]string{
		"route-a": "agency-a",
		"route-b": "agency-b",
	})
	app.GtfsService.RouteAgencyIndex.SetAgencyName(baseURL, "agency-a", "Agency A")
	app.GtfsService.RouteAgencyIndex.SetAgencyName(baseURL, "agency-b", "Agency B")

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	if _, ok := scope.(config.ServerScope); !ok {
		t.Fatalf("expected a ServerScope for an entry without agency_id, got %T", scope)
	}
	return app, server, scope
}

// TestServerScopeCountsEachVehicleOncePerTick is the regression test for the
// server-mode double-count: the vehicle pass must run once per server per
// tick, not once per live agency, or VehicleReportCount inflates by the live
// agency count on every tick, permanently.
func TestServerScopeCountsEachVehicleOncePerTick(t *testing.T) {
	app, server, scope := newTwoAgencyServerScope(t)

	app.collectForScope(context.Background(), server, scope)

	got := readCounter(t, metrics.VehicleReportCount, map[string]string{
		"vehicle_id":  "va",
		"agency_id":   "agency-a",
		"agency_name": "Agency A",
		"server_name": "multi",
		"server_url":  server.ObaBaseURL,
		"feed":        "0",
	})
	if got != 1 {
		t.Fatalf("expected vehicle va to be counted once per tick, got %v", got)
	}
}

// TestServerScopeAttributesVehiclesToOwningAgency checks the per-agency
// last-seen slots: each vehicle belongs to the agency that owns its route,
// not to every agency on the server.
func TestServerScopeAttributesVehiclesToOwningAgency(t *testing.T) {
	app, server, scope := newTwoAgencyServerScope(t)

	app.collectForScope(context.Background(), server, scope)

	lastSeen := app.MetricsService.VehicleLastSeen
	for agencyID, vehicleID := range map[string]string{"agency-a": "va", "agency-b": "vb"} {
		key := models.ServerKey(server.ObaBaseURL, agencyID)
		if got := lastSeen.Count(key); got != 1 {
			t.Fatalf("expected exactly 1 tracked vehicle for %s, got %d", agencyID, got)
		}
		if _, ok := lastSeen.Get(key, "0", vehicleID); !ok {
			t.Fatalf("expected %s to be tracked under %s", vehicleID, agencyID)
		}
	}
}
