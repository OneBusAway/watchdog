package app

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

// TestPruneStaleServersDropsDepartedServers is the leak test: when a server
// leaves the configuration nothing ever removed its parsed bundle, bounding
// box, realtime feed, route index, or last-seen vehicles, so a long-running
// Watchdog accumulated state for servers it no longer monitored.
func TestPruneStaleServersDropsDepartedServers(t *testing.T) {
	app := newTestApplication(t)

	const keptURL = "https://kept.example.com"
	const goneURL = "https://gone.example.com"

	kept := models.ObaServer{ServerName: "kept", AgencyID: "agency-a", ObaBaseURL: keptURL}
	gone := models.ObaServer{ServerName: "gone", AgencyID: "agency-b", ObaBaseURL: goneURL}

	for _, server := range []models.ObaServer{kept, gone} {
		app.GtfsService.StaticStore.Set(server.ServerKey(), &models.StaticData{})
		app.GtfsService.BoundingBoxStore.Set(server.ServerKey(), geo.BoundingBox{})
		app.GtfsService.RealtimeStore.Set(server.ServerKey(), &models.RealtimeData{})
		app.GtfsService.RouteAgencyIndex.Set(server.ObaBaseURL, map[string]string{"r1": server.AgencyID})
		app.MetricsService.VehicleLastSeen.Set(server.ServerKey(), "0", "v1", metrics.LastSeen{})
	}

	app.PruneStaleServers([]models.ObaServer{kept})

	if _, ok := app.GtfsService.StaticStore.Get(gone.ServerKey()); ok {
		t.Error("expected the departed server's static bundle to be pruned")
	}
	if _, ok := app.GtfsService.BoundingBoxStore.Get(gone.ServerKey()); ok {
		t.Error("expected the departed server's bounding box to be pruned")
	}
	if app.GtfsService.RealtimeStore.Get(gone.ServerKey()) != nil {
		t.Error("expected the departed server's realtime feed to be pruned")
	}
	if _, ok := app.GtfsService.RouteAgencyIndex.Get(goneURL, "r1"); ok {
		t.Error("expected the departed server's route index to be pruned")
	}
	if app.MetricsService.VehicleLastSeen.Count(gone.ServerKey()) != 0 {
		t.Error("expected the departed server's tracked vehicles to be pruned")
	}

	if _, ok := app.GtfsService.StaticStore.Get(kept.ServerKey()); !ok {
		t.Error("expected the configured server's static bundle to survive")
	}
	if _, ok := app.GtfsService.RouteAgencyIndex.Get(keptURL, "r1"); !ok {
		t.Error("expected the configured server's route index to survive")
	}
	if app.MetricsService.VehicleLastSeen.Count(kept.ServerKey()) != 1 {
		t.Error("expected the configured server's tracked vehicles to survive")
	}
}

// TestPruneStaleServersKeepsEveryAgencyOfAServerScopedEntry guards the rule
// that matters for server-mode: a server-scoped entry owns every key under its
// oba_base_url, including agencies discovered from agency.txt that the config
// never names.
func TestPruneStaleServersKeepsEveryAgencyOfAServerScopedEntry(t *testing.T) {
	app := newTestApplication(t)

	const baseURL = "https://multi.example.com"
	entry := models.ObaServer{ServerName: "multi", ObaBaseURL: baseURL}

	for _, agencyID := range []string{"", "agency-a", "agency-b"} {
		app.GtfsService.StaticStore.Set(models.ServerKey(baseURL, agencyID), &models.StaticData{})
	}
	app.GtfsService.StaticStore.Set(models.ServerKey("https://other.example.com", "agency-c"), &models.StaticData{})

	app.PruneStaleServers([]models.ObaServer{entry})

	for _, agencyID := range []string{"", "agency-a", "agency-b"} {
		key := models.ServerKey(baseURL, agencyID)
		if _, ok := app.GtfsService.StaticStore.Get(key); !ok {
			t.Errorf("expected %q to survive: its server is still configured", key)
		}
	}
	if _, ok := app.GtfsService.StaticStore.Get(models.ServerKey("https://other.example.com", "agency-c")); ok {
		t.Error("expected the unconfigured server's bundle to be pruned")
	}
}

// TestPruneStaleServersRetiresMetricSeries checks the other half: the stores
// are cleaned but /metrics would otherwise keep exposing the departed server's
// series at their final value forever.
func TestPruneStaleServersRetiresMetricSeries(t *testing.T) {
	app := newTestApplication(t)

	const goneURL = "https://retired.example.com"
	gone := models.ObaServer{ServerName: "retired", AgencyID: "agency-z", ObaBaseURL: goneURL}
	app.GtfsService.StaticStore.Set(gone.ServerKey(), &models.StaticData{})
	metrics.RealtimeVehiclePositions.WithLabelValues("agency-z", "Agency Z", "retired", goneURL).Set(5)
	metrics.ObaApiStatus.WithLabelValues("retired", goneURL).Set(1)

	app.PruneStaleServers(nil)

	if seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": goneURL}) != 0 {
		t.Error("expected the departed server's vehicle-position series to be deleted")
	}
	if seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": goneURL}) != 0 {
		t.Error("expected the departed server's api-status series to be deleted")
	}
}

// seriesCount reports how many series a collector exposes whose labels include
// every label in want. It never creates a series as a side effect.
func seriesCount(collector prometheus.Collector, want prometheus.Labels) int {
	ch := make(chan prometheus.Metric)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	count := 0
	for m := range ch {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			continue
		}
		labels := make(map[string]string, len(pb.Label))
		for _, l := range pb.Label {
			labels[l.GetName()] = l.GetValue()
		}
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			count++
		}
	}
	return count
}
