package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

// BenchmarkLargeServerCollection records peak goroutines, heap usage,
// allocations and cycle duration for a representative large server
// configuration: one server-scoped entry serving 50 agencies.
func BenchmarkLargeServerCollection(b *testing.B) {
	const agencies = 50

	rtBody := buildVehicleFeedProtobufB(b, "shared-vehicle")
	var rtCalls atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vehicles.pb":
			rtCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G104
			w.Write(rtBody)
		case "/api/where/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			ids := `"agency-00"`
			for i := 1; i < agencies; i++ {
				ids += `,"agency-` + twoDigits(i) + `"`
			}
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"entry":{"agencyIDs":[` + ids + `]}}}`))
		default:
			// Small delay so the benchmark distinguishes sequential from
			// concurrent agency checks without making the suite slow.
			time.Sleep(10 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			// #nosec G104
			w.Write([]byte(`{"code":200,"data":{"list":[],"entry":{"readableTime":"now"}}}`))
		}
	}))
	defer ts.Close()
	defer http.DefaultClient.CloseIdleConnections()

	app := newTestApplication(b)
	app.ConfigService.Config.FetchInterval = 30
	baseURL := ts.URL
	server := models.ObaServer{
		ServerName:  "bench-large",
		ObaBaseURL:  baseURL,
		ObaApiKey:   "test-key",
		GtfsRTFeeds: []models.GtfsRTFeed{{VehiclePositionURL: baseURL + "/vehicles.pb"}},
	}

	wholeWorld := geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180}
	routes := make(map[string]string, agencies)
	names := make(map[string]string, agencies)
	for i := 0; i < agencies; i++ {
		id := "agency-" + twoDigits(i)
		app.GtfsService.StaticStore.Set(models.ServerKey(baseURL, id), &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(models.ServerKey(baseURL, id), wholeWorld)
		routes["route-"+twoDigits(i)] = id
		names[id] = id
	}
	app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), wholeWorld)
	app.GtfsService.RouteAgencyIndex.Replace(models.ServerKey(baseURL, ""), routes, nil, names)

	scope := config.ResolveScope(server, app.GtfsService.StaticStore, app.GtfsService.RouteAgencyIndex)
	serverScope, ok := scope.(config.ServerScope)
	if !ok {
		b.Fatalf("expected ServerScope, got %T", scope)
	}
	if len(serverScope.StaticAgencies) != agencies {
		b.Fatalf("expected %d static agencies, got %d", agencies, len(serverScope.StaticAgencies))
	}

	// Warm up once so lazy init does not pollute the measurement.
	app.collectForServerScope(context.Background(), server, serverScope)

	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	baseGoroutines := runtime.NumGoroutine()

	var peak atomic.Int32
	peak.Store(int32(baseGoroutines))
	stopPeak := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := int32(runtime.NumGoroutine())
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
			case <-stopPeak:
				return
			}
		}
	}()

	allocsBefore := memBefore.Mallocs
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.collectForServerScope(context.Background(), server, serverScope)
	}
	b.StopTimer()
	cycleTotal := time.Since(start)
	close(stopPeak)
	runtime.ReadMemStats(&memAfter)

	peakGoroutines := peak.Load()
	b.ReportMetric(float64(peakGoroutines), "peak-goroutines")
	b.ReportMetric(float64(memAfter.HeapAlloc)/1024, "heap-kb")
	b.ReportMetric(float64(memAfter.Mallocs-allocsBefore)/float64(b.N), "allocs/op")
	b.ReportMetric(float64(cycleTotal.Milliseconds())/float64(b.N), "ms/cycle")
	b.Logf("agencies=%d cycles=%d total=%v per-cycle=%v peak-goroutines=%d heap-alloc-kb=%d allocs/op=%d rt-fetches=%d",
		agencies, b.N, cycleTotal, cycleTotal/time.Duration(b.N),
		peakGoroutines, memAfter.HeapAlloc/1024,
		(memAfter.Mallocs-allocsBefore)/uint64(b.N), rtCalls.Load())
}

func twoDigits(i int) string {
	return fmt.Sprintf("%02d", i)
}

func buildVehicleFeedProtobufB(b *testing.B, vehicleID string) []byte {
	b.Helper()
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
		b.Fatalf("marshal GTFS-RT feed: %v", err)
	}
	return data
}
