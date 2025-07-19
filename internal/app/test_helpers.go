package app

import (
	"log/slog"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/server"
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

	cfg := server.NewConfig(
		4000,
		"testing",
		[]models.ObaServer{*obaServer},
	)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	const cachePath = "../../testdata/gtfs.zip"
	staticData, err := gtfs.ParseGTFSFromCache(cachePath, obaServer.ID)
	if err != nil {
		t.Fatalf("Failed to parse GTFS data: %v", err)
	}
	if staticData == nil {
		t.Fatal("Parsed GTFS data is nil")
	}

	stops := staticData.Stops
	boundingBox, err := geo.ComputeBoundingBox(stops)

	if err != nil {
		t.Fatalf("Failed to compute bounding box: %v", err)
	}
	boundingBoxStore := geo.NewBoundingBoxStore()
	boundingBoxStore.Set(obaServer.ID, boundingBox)

	return &Application{
		Config: *cfg,
		Logger: logger,
		Version: "1.0.0",
		BoundingBoxStore: boundingBoxStore,
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
