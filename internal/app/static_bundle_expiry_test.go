package app

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// expiredTestFeed is a valid feed whose only service ended in January 2025.
func expiredTestFeed(t *testing.T) []byte {
	t.Helper()
	files := map[string]string{
		"agency.txt":     "agency_id,agency_name,agency_url,agency_timezone\nX,Expired Transit,https://example.com,UTC\n",
		"routes.txt":     "route_id,agency_id,route_short_name,route_type\nR,X,R,3\n",
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,1,1,20250101,20250131\n",
		"trips.txt":      "route_id,service_id,trip_id\nR,WK,T\n",
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,1\nS2,Two,2,2\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,2\n",
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestExpiredBundleWithholdsStaticDerivedMetrics(t *testing.T) {
	feed := expiredTestFeed(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.zip":
			w.Write(feed)
		case "/api/where/current-time.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"currentTime":1234567890000,"data":{"entry":{"readableTime":"now"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	app := newEmptyApplication(t)
	server := models.ObaServer{ServerName: "expired-test", AgencyID: "X", AgencyName: "Expired Transit", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/feed.zip"}}
	app.ConfigService.Config.UpdateConfig([]models.ObaServer{server})
	app.GtfsService.StaticStore.SetConfiguredServers([]models.ObaServer{server})
	app.GtfsService.DownloadGTFSBundles(context.Background(), []models.ObaServer{server}, 1)
	if _, ok := app.GtfsService.StaticStore.Get(server.ServerKey()); !ok {
		t.Fatal("fixture bundle was not published")
	}

	labels := map[string]string{"agency_id": server.AgencyID, "server_url": utils.SanitizeServerURL(server.ObaBaseURL)}
	// A value left over from before the bundle expired.
	metrics.RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)).Set(5)
	t.Cleanup(func() { metrics.DeleteSeriesForServer(utils.SanitizeServerURL(server.ObaBaseURL)) })

	app.collectOnce(context.Background())

	if _, found := gaugeValueFor(metrics.RealtimeVehiclePositions, labels); found {
		t.Fatal("an expired bundle left a stale vehicle count on /metrics")
	}
	if v, found := gaugeValueFor(metrics.GtfsScheduleAvailable, labels); !found || v != 0 {
		t.Fatalf("schedule available = %v (found %v), want 0 for an expired bundle", v, found)
	}
	if v, found := gaugeValueFor(metrics.GtfsStaticBundleUsable, labels); !found || v != 0 {
		t.Fatalf("bundle usable = %v (found %v), want 0", v, found)
	}
	if v, found := gaugeValueFor(metrics.BundleLatestExpirationGauge, labels); !found || v >= 0 {
		t.Fatalf("days until latest expiration = %v (found %v), want a negative value", v, found)
	}
	// The expiry was reported on the first tick and is not reported again.
	if app.BundleExpiry.transition(server.ServerKey(), true) {
		t.Fatal("the expiry was not recorded, so it would be reported again")
	}
}

func TestBundleExpiryReporterReportsChangesOnce(t *testing.T) {
	r := NewBundleExpiryReporter()
	const key = "https://oba.example|a"
	steps := []struct {
		expired, changed bool
	}{
		{false, false}, // never expired: nothing to say
		{true, true},   // expires: report
		{true, false},  // still expired: silent
		{false, true},  // recovers: report
		{false, false},
	}
	for i, step := range steps {
		if got := r.transition(key, step.expired); got != step.changed {
			t.Fatalf("step %d: changed = %v, want %v", i, got, step.changed)
		}
	}
	r.transition(key, true)
	r.Prune(func(string) bool { return false })
	if !r.transition(key, true) {
		t.Fatal("pruned state was kept")
	}
}
