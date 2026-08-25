package app

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

// TestOnConfigUpdatedIgnoresAnEmptyConfig is the blast-radius guard. The
// refresh callback fires on every successful load and the decoder drops invalid
// entries rather than failing the document, so a config endpoint briefly
// serving "[]" reaches this function as an empty slice. Acting on it would
// discard every cached bundle and retire the whole /metrics surface.
func TestOnConfigUpdatedIgnoresAnEmptyConfig(t *testing.T) {
	app := newTestApplication(t)

	const url = "https://still-configured.example.com"
	configured := models.ObaServer{ServerName: "keep", AgencyID: "agency-a", ObaBaseURL: url}
	app.GtfsService.StaticStore.Set(configured.ServerKey(), &models.StaticData{})
	metrics.RealtimeVehiclePositions.WithLabelValues("agency-a", "Agency A", "keep", url).Set(4)

	app.OnConfigUpdated(context.Background(), nil)

	if _, ok := app.GtfsService.StaticStore.Get(configured.ServerKey()); !ok {
		t.Error("an empty refreshed config must not discard cached bundles")
	}
	if seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": url}) == 0 {
		t.Error("an empty refreshed config must not retire the /metrics surface")
	}
}

// TestOnConfigUpdatedPrunesAndDetectsOnANonEmptyConfig pins that the guard
// above did not simply disable the callback: a real config change still prunes
// the departed server and reports the newcomer.
func TestOnConfigUpdatedPrunesAndDetectsOnANonEmptyConfig(t *testing.T) {
	app := newTestApplication(t)

	const goneURL = "https://departing.example.com"
	const keptURL = "https://arriving.example.com"
	gone := models.ObaServer{ServerName: "gone", AgencyID: "agency-x", ObaBaseURL: goneURL}
	kept := models.ObaServer{ServerName: "kept", AgencyID: "agency-y", ObaBaseURL: keptURL}

	app.GtfsService.StaticStore.Set(gone.ServerKey(), &models.StaticData{})
	metrics.RealtimeVehiclePositions.WithLabelValues("agency-x", "Agency X", "gone", goneURL).Set(9)

	app.OnConfigUpdated(context.Background(), []models.ObaServer{kept})

	if _, ok := app.GtfsService.StaticStore.Get(gone.ServerKey()); ok {
		t.Error("expected the departed server's bundle to be pruned")
	}
	if seriesCount(metrics.RealtimeVehiclePositions, prometheus.Labels{"server_url": goneURL}) != 0 {
		t.Error("expected the departed server's series to be retired")
	}
	// The newcomer was reported exactly once; a second identical refresh must
	// not re-report it, or every refresh re-downloads its bundles.
	if again := app.NewlyAddedServers([]models.ObaServer{kept}); len(again) != 0 {
		t.Errorf("expected no newcomers on an unchanged config, got %d", len(again))
	}
}
