package app

import (
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

func newTestApplication(t *testing.T) *Application {
	t.Helper()

	obaServer := models.NewObaServer(
		"Test Server",
		"Test Agency",
		"test-agency",
		"https://test.example.com",
		"test-key",
		[]string{"https://gtfs.example.com"},
		[]models.GtfsRTFeed{{VehiclePositionURL: "https://vehicle.example.com"}},
	)

	cfg := config.NewConfig(
		4000,
		"testing",
		[]models.ObaServer{*obaServer},
	)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	const staticDataPath = "../../testdata/gtfs.zip"
	fileBytes, err := os.ReadFile(staticDataPath)
	if err != nil {
		t.Fatalf("Failed to read GTFS fixture: %v", err)
	}
	staticBundle, err := remoteGtfs.ParseStatic(fileBytes, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatalf("Failed to parse GTFS data: %v", err)
	}
	if staticBundle == nil {
		t.Fatal("Parsed GTFS data is nil")
	}

	staticData := models.NewStaticData(staticBundle)
	staticStore := gtfs.NewStaticStore()
	staticStore.Set(obaServer.ServerKey(), staticData)

	stops := staticData.Stops
	boundingBox, err := geo.ComputeBoundingBox(stops)

	if err != nil {
		t.Fatalf("Failed to compute bounding box: %v", err)
	}
	boundingBoxStore := geo.NewBoundingBoxStore()
	boundingBoxStore.Set(obaServer.ServerKey(), boundingBox)

	const realtimeDataPath = "../../testdata/gtfs_rt_feed_vehicles.pb"
	data, err := os.ReadFile(realtimeDataPath)
	if err != nil {
		t.Fatalf("Failed to read GTFS-RT fixture: %v", err)
	}
	gtfsRT, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		t.Fatalf("Failed to parse GTFS-RT data: %v", err)
	}
	if gtfsRT == nil {
		t.Fatal("Parsed GTFS-RT data is nil")
	}
	realtimeData := models.NewRealtimeData(gtfsRT)
	realtimeStore := gtfs.NewRealtimeStore()
	realtimeStore.Set(obaServer.ServerKey(), realtimeData)

	routeAgencyIndex := gtfs.NewRouteAgencyIndex()
	vehicleLastSeen := metrics.NewVehicleLastSeen()
	unmatchedStopTracker := metrics.NewUnmatchedStopTracker()
	backoffStore := config.NewBackoffStore()
	obaSDKClientCache := NewObaSDKClientCache(client)
	return &Application{
		ConfigService:  config.NewConfigService(logger, client, cfg, backoffStore),
		GtfsService:    gtfs.NewGtfsService(staticStore, realtimeStore, boundingBoxStore, routeAgencyIndex, logger, client),
		MetricsService: metrics.NewMetricsService(staticStore, realtimeStore, boundingBoxStore, routeAgencyIndex, vehicleLastSeen, unmatchedStopTracker, logger, client, obaSDKClientCache.For),
		Version:        "1.0.0",
		Logger:         logger,
	}
}

func getMetricsForTesting(t *testing.T, metric *prometheus.GaugeVec) {
	t.Helper()

	ch := make(chan prometheus.Metric)
	go func() {
		metric.Collect(ch)
		close(ch)
	}()

	for m := range ch {
		t.Logf("Found metric: %v", m.Desc())
	}
}

// readTestFixture reads a fixture file, failing the test if it cannot be read.
func readTestFixture(t *testing.T, path string) []byte {
	t.Helper()
	// Safe: path is a test-local constant, never user input.
	// #nosec G304
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", path, err)
	}
	return data
}

// readCounter returns the current value of a CounterVec series. Like the
// prometheus client's own With, it materializes the series if it is absent, so
// a returned 0 means "no increments", not "never emitted".
func readCounter(t *testing.T, vec *prometheus.CounterVec, labels map[string]string) float64 {
	t.Helper()

	ch := make(chan prometheus.Metric, 1)
	vec.With(labels).Collect(ch)

	pb := &dto.Metric{}
	if err := (<-ch).Write(pb); err != nil {
		t.Fatalf("failed to read counter: %v", err)
	}
	return pb.GetCounter().GetValue()
}
