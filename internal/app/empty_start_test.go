package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

// newEmptyApplication builds an Application the way main does when a
// --config-url source is empty, unreachable, or entirely invalid at boot.
func newEmptyApplication(t *testing.T) *Application {
	t.Helper()
	cfg := config.NewConfig(4000, "testing", nil)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := &http.Client{Timeout: 5 * time.Second}
	return New(cfg, logger, client, "test", config.NewDroppedServersStore())
}

// TestEmptyStartPicksUpFirstServersOnRefresh pins the 0 -> N transition: a
// process that booted with no servers must treat the first non-empty refresh
// exactly like any other newcomer and download its static bundle at once.
func TestEmptyStartPicksUpFirstServersOnRefresh(t *testing.T) {
	bundle, err := os.ReadFile("../../testdata/gtfs.zip")
	if err != nil {
		t.Fatal(err)
	}
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(bundle)
	}))
	defer feed.Close()

	app := newEmptyApplication(t)
	server := models.NewObaServer("Late", "Sound Transit", "40", "https://late.example.com", "key", []string{feed.URL}, nil)

	app.ConfigService.Config.UpdateConfig([]models.ObaServer{*server})
	app.OnConfigUpdated(context.Background(), []models.ObaServer{*server})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := app.GtfsService.StaticStore.Get(server.ServerKey()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first server to arrive after an empty start never had its static bundle downloaded")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if again := app.NewlyAddedServers([]models.ObaServer{*server}); len(again) != 0 {
		t.Errorf("expected the server to be recorded as known, got %d newcomers", len(again))
	}
}

// TestConfiguredServersGaugeTracksConfig pins that the gauge distinguishes
// "up but nothing configured" from "healthy".
func TestConfiguredServersGaugeTracksConfig(t *testing.T) {
	app := newEmptyApplication(t)
	app.MetricsService.ReportConfiguredServers(0)
	if got := configuredServers(t); got != 0 {
		t.Fatalf("configured servers = %v, want 0", got)
	}

	a := models.NewObaServer("A", "A", "a", "https://a.example.com", "k", nil, nil)
	b := models.NewObaServer("B", "B", "b", "https://b.example.com", "k", nil, nil)
	app.OnConfigUpdated(context.Background(), []models.ObaServer{*a, *b})
	if got := configuredServers(t); got != 2 {
		t.Fatalf("configured servers after refresh = %v, want 2", got)
	}

	// An empty refresh is ignored, so the gauge keeps the count still in force.
	app.OnConfigUpdated(context.Background(), nil)
	if got := configuredServers(t); got != 2 {
		t.Fatalf("configured servers after ignored empty refresh = %v, want 2", got)
	}
}

func configuredServers(t *testing.T) float64 { return readGauge(t, metrics.WatchdogConfiguredServers) }

func collectionLastCompleted(t *testing.T) float64 {
	return readGauge(t, metrics.WatchdogCollectionLastCompleted)
}

func readGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	pb := &dto.Metric{}
	if err := g.Write(pb); err != nil {
		t.Fatal(err)
	}
	return pb.GetGauge().GetValue()
}

// TestCollectionTickOverZeroServersIsNotHealthy pins that a tick over no
// servers neither panics nor advances the last-completed timestamp: OBACloud
// reads a fresh timestamp plus up==1 as "Watchdog current".
func TestCollectionTickOverZeroServersIsNotHealthy(t *testing.T) {
	app := newEmptyApplication(t)
	metrics.WatchdogCollectionLastCompleted.Set(0)

	app.collectOnce(context.Background())

	if got := collectionLastCompleted(t); got != 0 {
		t.Fatalf("last completed = %v, want 0 (no servers collected)", got)
	}
}
