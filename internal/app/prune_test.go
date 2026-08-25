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

// TestPruneStaleServersDropsBackoffAndUnmatchedStopState covers the two shared
// stores the first version of PruneStaleServers missed. Neither expires on its
// own in a way that helps: backoff state has no TTL at all, and unmatched-stop
// entries wait out a 24h TTL for a server that will never report again.
func TestPruneStaleServersDropsBackoffAndUnmatchedStopState(t *testing.T) {
	app := newTestApplication(t)

	const keptURL = "https://backoff-kept.example.com"
	const goneURL = "https://backoff-gone.example.com"

	kept := models.ObaServer{ServerName: "kept", AgencyID: "agency-a", ObaBaseURL: keptURL}
	gone := models.ObaServer{ServerName: "gone", AgencyID: "agency-b", ObaBaseURL: goneURL}

	for _, server := range []models.ObaServer{kept, gone} {
		app.ConfigService.BackoffStore.UpdateBackoff(server.ServerKey())
		app.MetricsService.UnmatchedStopTracker.RecordLastSeen(
			server.ServerKey(), server.AgencyID, server.ServerName, server.ServerName,
			server.ObaBaseURL, "stop-1", "Stop 1", "1.000000", "2.000000")
		app.MetricsService.UnmatchedStopTracker.RecordClusterSeen(
			server.ServerKey(), server.AgencyID, server.ServerName, server.ServerName,
			server.ObaBaseURL, "station-1", "cluster-1", "1.000000", "2.000000")
	}

	app.PruneStaleServers([]models.ObaServer{kept})

	if _, ok := app.ConfigService.BackoffStore.NextRetryAt(gone.ServerKey()); ok {
		t.Error("expected the departed server's backoff state to be pruned")
	}
	if _, ok := app.MetricsService.UnmatchedStopTracker.Entries[gone.ServerKey()]; ok {
		t.Error("expected the departed server's tracked unmatched stops to be pruned")
	}
	if _, ok := app.MetricsService.UnmatchedStopTracker.Clusters[gone.ServerKey()]; ok {
		t.Error("expected the departed server's tracked unmatched-stop clusters to be pruned")
	}

	if _, ok := app.ConfigService.BackoffStore.NextRetryAt(kept.ServerKey()); !ok {
		t.Error("expected the configured server's backoff state to survive")
	}
	if _, ok := app.MetricsService.UnmatchedStopTracker.Entries[kept.ServerKey()]; !ok {
		t.Error("expected the configured server's tracked unmatched stops to survive")
	}
	if _, ok := app.MetricsService.UnmatchedStopTracker.Clusters[kept.ServerKey()]; !ok {
		t.Error("expected the configured server's tracked unmatched-stop clusters to survive")
	}
}

// TestPruneStaleServersRetiresSeriesForABackoffOnlyServer is the reason the
// newly-pruned keys join the `removed` set. A server whose ping never
// succeeded has no bundle, no bounding box and no vehicles — backoff state is
// the only trace it leaves in the stores — yet it does publish series
// (oba_api_status 0). If its key were pruned without being reported, nothing
// would drive DeleteSeriesForServer and /metrics would keep exposing a
// permanently-failing server that is no longer configured, which reads to an
// alert as an outage rather than a removal.
func TestPruneStaleServersRetiresSeriesForABackoffOnlyServer(t *testing.T) {
	app := newTestApplication(t)

	const goneURL = "https://never-collected.example.com"
	gone := models.ObaServer{ServerName: "never-collected", AgencyID: "agency-n", ObaBaseURL: goneURL}

	app.ConfigService.BackoffStore.UpdateBackoff(gone.ServerKey())
	metrics.ObaApiStatus.WithLabelValues(gone.ServerName, goneURL).Set(0)

	app.PruneStaleServers(nil)

	if seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": goneURL}) != 0 {
		t.Error("expected the departed backoff-only server's api-status series to be deleted")
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

// TestPruneStaleServersRetiresSeriesResurrectedByAnInFlightTick is the race
// regression test. StartMetricsCollection snapshots the server list at the top
// of a tick, so a config refresh can land mid-tick and the still-in-flight tick
// can write a departed server's gauges again after PruneStaleServers retired
// them. Deletion driven purely by which store keys were removed never notices:
// the resurrecting write here is a failed ping, which sets oba_api_status to 0
// and touches no pruned store, so the next prune sees nothing removed and the
// series sits on /metrics frozen at 0 forever — which reads to an alert as a
// hard outage rather than an absent server.
func TestPruneStaleServersRetiresSeriesResurrectedByAnInFlightTick(t *testing.T) {
	app := newTestApplication(t)

	const keptURL = "https://inflight-kept.example.com"
	const goneURL = "https://inflight-gone.example.com"

	kept := models.ObaServer{ServerName: "kept", AgencyID: "agency-a", ObaBaseURL: keptURL}
	gone := models.ObaServer{ServerName: "gone", AgencyID: "agency-b", ObaBaseURL: goneURL}

	// Both servers are configured and reporting.
	app.GtfsService.StaticStore.Set(gone.ServerKey(), &models.StaticData{})
	metrics.ObaApiStatus.WithLabelValues(gone.ServerName, goneURL).Set(1)
	metrics.ObaApiStatus.WithLabelValues(kept.ServerName, keptURL).Set(1)
	app.PruneStaleServers([]models.ObaServer{kept, gone})

	// gone leaves the configuration: its store keys go, and with them its
	// series.
	app.PruneStaleServers([]models.ObaServer{kept})
	if got := seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": goneURL}); got != 0 {
		t.Fatalf("expected the departed server's api-status series to be deleted, got %d", got)
	}

	// The tick that was already iterating the old server list reaches gone and
	// writes its gauge again. The ping failed, so backoff is the only thing
	// updated and nothing lands in a store PruneStaleServers walks.
	metrics.ObaApiStatus.WithLabelValues(gone.ServerName, goneURL).Set(0)

	app.PruneStaleServers([]models.ObaServer{kept})

	if got := seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": goneURL}); got != 0 {
		t.Errorf("expected the resurrected series of a departed server to be retired by the next prune, got %d series", got)
	}
	if got := seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": keptURL}); got != 1 {
		t.Errorf("expected the configured server's api-status series to survive, got %d series", got)
	}
}

// TestPruneStaleServersNeverRetiresAConfiguredServer is the safety half of the
// self-healing rule: remembering every server URL ever configured must not
// leak into deleting series for one that is still configured, however many
// times the refresh loop prunes.
func TestPruneStaleServersNeverRetiresAConfiguredServer(t *testing.T) {
	app := newTestApplication(t)

	const keptURL = "https://always-configured.example.com"
	kept := models.ObaServer{ServerName: "always", AgencyID: "agency-k", ObaBaseURL: keptURL}

	for i := 0; i < 3; i++ {
		metrics.ObaApiStatus.WithLabelValues(kept.ServerName, keptURL).Set(1)
		metrics.RealtimeVehiclePositions.WithLabelValues(kept.AgencyID, kept.AgencyName, kept.ServerName, keptURL).Set(7)

		app.PruneStaleServers([]models.ObaServer{kept})

		if got := seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": keptURL}); got != 1 {
			t.Fatalf("prune %d: expected the configured server's api-status series to survive, got %d series", i, got)
		}
		if got := seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": keptURL}); got != 1 {
			t.Fatalf("prune %d: expected the configured server's vehicle-position series to survive, got %d series", i, got)
		}
	}
}

// TestPruneStaleServersRetiresOnlyTheDepartedAgencyOnASharedServer guards the
// store-key-driven agency-level deletion, which the self-healing URL memory
// must not replace: when one agency-scoped entry leaves a base URL that
// another entry still claims, only the departed agency's own series go.
func TestPruneStaleServersRetiresOnlyTheDepartedAgencyOnASharedServer(t *testing.T) {
	app := newTestApplication(t)

	const sharedURL = "https://shared-agencies.example.com"
	keptAgency := models.ObaServer{ServerName: "shared", AgencyID: "agency-keep", ObaBaseURL: sharedURL}
	goneAgency := models.ObaServer{ServerName: "shared", AgencyID: "agency-drop", ObaBaseURL: sharedURL}

	for _, server := range []models.ObaServer{keptAgency, goneAgency} {
		app.GtfsService.StaticStore.Set(server.ServerKey(), &models.StaticData{})
		metrics.RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, sharedURL).Set(3)
	}
	metrics.ObaApiStatus.WithLabelValues("shared", sharedURL).Set(1)
	app.PruneStaleServers([]models.ObaServer{keptAgency, goneAgency})

	app.PruneStaleServers([]models.ObaServer{keptAgency})

	if got := seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": sharedURL, "agency_id": "agency-drop"}); got != 0 {
		t.Errorf("expected the departed agency's series to be deleted, got %d series", got)
	}
	if got := seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": sharedURL, "agency_id": "agency-keep"}); got != 1 {
		t.Errorf("expected the surviving agency's series to be left alone, got %d series", got)
	}
	if got := seriesCount(metrics.ObaApiStatus, prometheus.Labels{"server_url": sharedURL}); got != 1 {
		t.Errorf("expected the still-configured server's api-status series to be left alone, got %d series", got)
	}
}

// TestPruneStaleServersRetiresServerScopedSeriesOnAScopeChange covers the
// conversion case: a server-scoped entry is replaced by agency-scoped entries
// on the same oba_base_url. The URL stays configured so the server never counts
// as departed, but nothing writes its agency-less series any more — including
// gtfs_rt_unattributed_vehicles_count, which carries no agency_id label at all
// and so cannot be reached by an agency-scoped deletion.
func TestPruneStaleServersRetiresServerScopedSeriesOnAScopeChange(t *testing.T) {
	app := newTestApplication(t)

	const url = "https://converted.example.com"
	serverScoped := models.ObaServer{ServerName: "multi", ObaBaseURL: url}
	agencyScoped := models.ObaServer{ServerName: "multi", ObaBaseURL: url, AgencyID: "agency-a", AgencyName: "Agency A"}

	// State a server-scoped entry leaves behind.
	app.GtfsService.StaticStore.Set(models.ServerKey(url, ""), &models.StaticData{})
	app.GtfsService.StaticStore.Set(models.ServerKey(url, "agency-a"), &models.StaticData{})
	metrics.InvalidVehicleCoordinatesGauge.WithLabelValues("", "", "multi", url).Set(3)
	metrics.GtfsRtUnattributedVehicles.WithLabelValues("multi", url).Set(7)
	metrics.RealtimeVehiclePositions.WithLabelValues("agency-a", "Agency A", "multi", url).Set(5)

	// While still server-scoped, none of it may be retired.
	app.PruneStaleServers([]models.ObaServer{serverScoped})
	if seriesCount(metrics.GtfsRtUnattributedVehicles, prometheus.Labels{"server_url": url}) == 0 {
		t.Fatal("a still-server-scoped entry must keep its unattributed gauge")
	}

	// Converting to agency-scoped orphans the agency-less series.
	app.PruneStaleServers([]models.ObaServer{agencyScoped})

	if seriesCount(metrics.InvalidVehicleCoordinatesGauge, prometheus.Labels{"server_url": url, "agency_id": ""}) != 0 {
		t.Error("expected the agency_id=\"\" catch-all to be retired after the scope change")
	}
	if seriesCount(metrics.GtfsRtUnattributedVehicles, prometheus.Labels{"server_url": url}) != 0 {
		t.Error("expected the server-scoped unattributed gauge to be retired after the scope change")
	}
	if seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": url, "agency_id": "agency-a"}) == 0 {
		t.Error("the surviving agency's series must not be retired")
	}
}
