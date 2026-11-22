package metrics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// TestFetchObaAPIMetricsWithheldSkipsStopLookup covers a bundle that is expired
// or that the backend does not serve: unmatched stops must not be attributed
// against it, while the metrics that do not read the bundle keep reporting.
func TestFetchObaAPIMetricsWithheldSkipsStopLookup(t *testing.T) {
	const agency, name, serverName = "withheld-agency", "Withheld Agency", "withheld-server"
	lat, lon := 1.0, 2.0
	response := `{"code":200,"data":{"entry":{"agencyIDs":["withheld-agency"],` +
		`"stopIDsUnmatchedCount":{"withheld-agency":1},"stopIDsUnmatched":{"withheld-agency":["stop-1"]}}}}`
	server := setupObaServer(t, response, http.StatusOK)
	defer server.Close()
	serverURL := utils.SanitizeServerURL(server.URL)
	defer DeleteSeriesForServer(serverURL)

	staticStore := gtfs.NewStaticStore()
	staticStore.Set(models.ServerKey(server.URL, agency), &models.StaticData{
		Stops: []remoteGtfs.Stop{{Id: "stop-1", Latitude: &lat, Longitude: &lon, Name: "Stop One"}},
	})
	tracker := NewUnmatchedStopTracker()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fetch := func(withheld bool) {
		t.Helper()
		if err := fetchObaAPIMetrics(context.Background(), agency, name, serverName, server.URL, "key",
			&http.Client{Timeout: 10 * time.Second}, staticStore, logger, tracker, nil, withheld); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}
	labels := map[string]string{"agency_id": agency, "agency_name": name, "server_name": serverName, "server_url": serverURL}
	infoLabels := []string{agency, name, serverName, serverURL, "stop-1", "Stop One", "1.000000", "2.000000"}

	ObaUnmatchedStopUnresolved.WithLabelValues(agency, name, serverName, serverURL).Set(3)
	fetch(true)
	if ObaUnmatchedStopUnresolved.DeleteLabelValues(agency, name, serverName, serverURL) {
		t.Fatal("unresolved gauge kept a value while the bundle was withheld")
	}
	if ObaUnmatchedStopInfo.DeleteLabelValues(infoLabels...) {
		t.Fatal("stop was attributed against a withheld bundle")
	}
	if got, err := getMetricValue(ObaStopsUnmatched, labels); err != nil || got != 1 {
		t.Fatalf("unmatched count = %v, err = %v; want 1 while withheld", got, err)
	}

	fetch(false)
	if got, err := getMetricValue(ObaUnmatchedStopUnresolved, labels); err != nil || got != 0 {
		t.Fatalf("unresolved = %v, err = %v; want 0 after resuming", got, err)
	}
	if !ObaUnmatchedStopInfo.DeleteLabelValues(infoLabels...) {
		t.Fatal("stop lookup did not resume")
	}
}
