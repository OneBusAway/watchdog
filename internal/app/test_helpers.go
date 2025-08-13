package app

import (
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	remoteGtfs "github.com/jamespfennell/gtfs"
	"github.com/prometheus/client_golang/prometheus"
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
		1,
		"https://test.example.com",
		"test-key",
		"",
		"",
		"",
		"",
		"",
		"",
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
	staticData, err := remoteGtfs.ParseStatic(fileBytes, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		t.Fatalf("Failed to parse GTFS data: %v", err)
	}
	if staticData == nil {
		t.Fatal("Parsed GTFS data is nil")
	}

	staticStore := gtfs.NewStaticStore()
	staticStore.Set(obaServer.ID, staticData)

	stops := staticData.Stops
	boundingBox, err := geo.ComputeBoundingBox(stops)

	if err != nil {
		t.Fatalf("Failed to compute bounding box: %v", err)
	}
	boundingBoxStore := geo.NewBoundingBoxStore()
	boundingBoxStore.Set(obaServer.ID, boundingBox)

	const realtimeDataPath = "../../testdata/gtfs_rt_feed_vehicles.pb"
	data, err := os.ReadFile(realtimeDataPath)
	if err != nil {
		t.Fatalf("Failed to read GTFS-RT fixture: %v", err)
	}
	realtimeData, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		t.Fatalf("Failed to parse GTFS-RT data: %v", err)
	}
	if realtimeData == nil {
		t.Fatal("Parsed GTFS-RT data is nil")
	}
	realtimeStore := gtfs.NewRealtimeStore()
	realtimeStore.Set(realtimeData)

	vehicleLastSeen := metrics.NewVehicleLastSeen()

	return &Application{
		ConfigService:  config.NewConfigService(logger, client, cfg),
		GtfsService:    gtfs.NewGtfsService(staticStore, realtimeStore, boundingBoxStore, logger, client),
		MetricsService: metrics.NewMetricsService(staticStore, realtimeStore, boundingBoxStore, vehicleLastSeen, logger, client),
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
