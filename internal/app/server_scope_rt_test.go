package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/models"
)

// TestServerScopeRTFeedFetchedOncePerTick verifies the once-fetch design:
// with 3 live agencies in server-mode, the RT endpoint is hit exactly once
// per tick (not three times), and the merged data lands under the
// server-scoped key — the single key the once-per-server vehicle pass reads.
func TestServerScopeRTFeedFetchedOncePerTick(t *testing.T) {
	rtBody := buildVehicleFeedProtobuf(t, "shared-vehicle")
	var (
		mu       sync.Mutex
		rtCalls  int
		allCalls int
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		allCalls++
		count := rtCalls
		mu.Unlock()

		switch {
		case r.URL.Path == "/api/where/current-time.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"data":{"entry":{"readableTime":"now"}}}`))
		case r.URL.Path == "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":["agency-A","agency-B","agency-C"]}}}`))
		case r.URL.Path == "/vehicles.pb":
			// RT path. Count just the RT hits.
			mu.Lock()
			rtCalls = count + 1
			mu.Unlock()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(rtBody)
		default:
			// Other OBA SDK paths (e.g. /api/where/vehicles-for-agency.json).
			// Return an empty JSON object so the SDK doesn't choke on the
			// protobuf body; we don't care about those metrics here.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{}}}`))
		}
	}))
	defer ts.Close()
	defer http.DefaultClient.CloseIdleConnections()

	app := newTestApplication(t)
	baseURL := ts.URL

	// Reconfigure the application for server-mode with 3 declared agencies.
	// The static store must have one entry per agency (this is what the
	// 24h static-download path populates).
	for _, agencyID := range []string{"agency-A", "agency-B", "agency-C"} {
		key := models.ServerKey(baseURL, agencyID)
		app.GtfsService.StaticStore.Set(key, &models.StaticData{})
		app.GtfsService.RouteAgencyIndex.Set(baseURL, map[string]string{
			"route-1": agencyID,
		})
		app.GtfsService.RouteAgencyIndex.SetAgencyName(baseURL, agencyID, agencyID)
	}

	// Override the server's URL + RT-feed URL to point at our stub.
	server := app.ConfigService.Config.Servers[0]
	server.ObaBaseURL = baseURL
	server.GtfsRTFeeds = []models.GtfsRTFeed{{
		VehiclePositionURL: baseURL + "/vehicles.pb",
	}}

	// Build the ServerScope directly (skipping ResolveScope since we just
	// want to test the fan-out path).
	staticAgencies := []config.AgencyIdentity{
		{AgencyID: "agency-A", AgencyName: "agency-A"},
		{AgencyID: "agency-B", AgencyName: "agency-B"},
		{AgencyID: "agency-C", AgencyName: "agency-C"},
	}
	scope := config.ServerScope{
		ServerMeta:     config.MetaFrom(server),
		StaticAgencies: staticAgencies,
	}

	// Tick the fan-out.
	app.collectForServerScope(context.Background(), server, scope)

	mu.Lock()
	gotRT := rtCalls
	gotAll := allCalls
	mu.Unlock()

	if gotRT != 1 {
		t.Fatalf("expected exactly 1 RT fetch for 3 agencies in server-mode, got %d (total %d)", gotRT, gotAll)
	}

	stored := app.GtfsService.RealtimeStore.Get(server.ServerKey())
	if stored == nil {
		t.Fatalf("expected the merged feed under the server-scoped key %q", server.ServerKey())
	}
	if len(stored.Vehicles) != 1 {
		t.Fatalf("expected 1 vehicle in merged RT data, got %d", len(stored.Vehicles))
	}

	// Per-agency realtime entries are deliberately absent: attribution is the
	// vehicle pass's job, and a copy per agency is what used to invite a
	// per-agency pass that double-counted every vehicle.
	for _, agencyID := range []string{"agency-A", "agency-B", "agency-C"} {
		if app.GtfsService.RealtimeStore.Get(models.ServerKey(baseURL, agencyID)) != nil {
			t.Fatalf("expected no realtime entry under %s", agencyID)
		}
	}
}

func buildVehicleFeedProtobuf(t *testing.T, vehicleID string) []byte {
	t.Helper()
	feed := &gtfsrt.FeedMessage{
		Header: &gtfsrt.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Incrementality:      gtfsrt.FeedHeader_FULL_DATASET.Enum(),
			Timestamp:           proto.Uint64(uint64(time.Now().Unix())),
		},
		Entity: []*gtfsrt.FeedEntity{
			{
				Id: proto.String("v-1"),
				Vehicle: &gtfsrt.VehiclePosition{
					Vehicle:   &gtfsrt.VehicleDescriptor{Id: proto.String(vehicleID)},
					Position:  &gtfsrt.Position{Latitude: proto.Float32(1.0), Longitude: proto.Float32(2.0)},
					Timestamp: proto.Uint64(uint64(time.Now().Unix())),
				},
			},
		},
	}
	data, err := proto.Marshal(feed)
	if err != nil {
		t.Fatalf("marshal GTFS-RT feed: %v", err)
	}
	return data
}
