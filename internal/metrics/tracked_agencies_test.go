package metrics

import (
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

func TestReportTrackedAgenciesCountsStaticAgencyIDsPerServer(t *testing.T) {
	resetTrackedAgenciesMetrics()
	store := gtfs.NewStaticStore()
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: "https://multi.example"}
	data := &models.StaticData{Agencies: []remoteGtfs.Agency{
		{Id: "A", Name: "Agency A"}, {Id: "B", Name: "Agency B"},
	}}
	store.Set(models.ServerKey(server.ObaBaseURL, "A"), data)
	store.Set(models.ServerKey(server.ObaBaseURL, "B"), data)

	reportTrackedAgencies([]models.ObaServer{server}, store)

	if got := trackedCountValue(t, server); got != 2 {
		t.Fatalf("static agency count = %v, want 2", got)
	}
	series := trackedAgencySeries(t)
	if len(series) != 2 {
		t.Fatalf("agency info series = %d, want 2: %+v", len(series), series)
	}
	for _, agency := range []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}, {Id: "B", Name: "Agency B"}} {
		if _, ok := findTrackedAgency(series, agency.Id, agency.Name, server.ObaBaseURL); !ok {
			t.Errorf("missing inventory row for %s: %+v", agency.Id, series)
		}
	}
}

func TestReportTrackedAgenciesDeduplicatesAgencyAcrossStaticFeeds(t *testing.T) {
	resetTrackedAgenciesMetrics()
	store := gtfs.NewStaticStore()
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: "https://multi.example"}
	data := &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}}}
	store.Set(models.ServerKey(server.ObaBaseURL, "A"), data)
	store.Set(models.ServerKey(server.ObaBaseURL, "A")+"-second-feed", data)

	reportTrackedAgencies([]models.ObaServer{server}, store)

	if got := trackedCountValue(t, server); got != 1 {
		t.Fatalf("duplicate agency count = %v, want 1", got)
	}
	if got := len(trackedAgencySeries(t)); got != 1 {
		t.Fatalf("agency info rows = %d, want 1", got)
	}
}

func TestReportTrackedAgenciesAgencyModeIsOneOnlyWhenStaticSnapshotContainsConfiguredAgency(t *testing.T) {
	resetTrackedAgenciesMetrics()
	store := gtfs.NewStaticStore()
	server := models.ObaServer{ServerName: "single", AgencyID: "A", AgencyName: "Agency A", ObaBaseURL: "https://single.example"}
	reportTrackedAgencies([]models.ObaServer{server}, store)
	if got := trackedCountValue(t, server); got != 0 {
		t.Fatalf("agency count without static snapshot = %v, want 0", got)
	}

	store.Set(server.ServerKey(), &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}}})
	reportTrackedAgencies([]models.ObaServer{server}, store)
	if got := trackedCountValue(t, server); got != 1 {
		t.Fatalf("agency count with configured agency snapshot = %v, want 1", got)
	}
	if got := len(trackedAgencySeries(t)); got != 1 {
		t.Fatalf("agency mode inventory rows = %d, want 1", got)
	}

	store.Set(server.ServerKey(), &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "B", Name: "Other Agency"}}})
	reportTrackedAgencies([]models.ObaServer{server}, store)
	if got := trackedCountValue(t, server); got != 0 {
		t.Fatalf("agency count when configured ID is absent = %v, want 0", got)
	}
	if got := len(trackedAgencySeries(t)); got != 0 {
		t.Fatalf("agency mode inventory rows when configured ID is absent = %d, want 0", got)
	}
}

func TestReportTrackedAgenciesCombinesAgencyModeEntriesForOneServer(t *testing.T) {
	resetTrackedAgenciesMetrics()
	store := gtfs.NewStaticStore()
	alpha := models.ObaServer{ServerName: "shared", AgencyID: "A", AgencyName: "Agency A", ObaBaseURL: "https://shared.example"}
	beta := models.ObaServer{ServerName: "shared", AgencyID: "B", AgencyName: "Agency B", ObaBaseURL: "https://shared.example"}
	store.SetConfiguredServers([]models.ObaServer{alpha, beta})
	store.Set(alpha.ServerKey(), &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}}})
	store.Set(beta.ServerKey(), &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "B", Name: "Agency B"}}})

	reportTrackedAgencies([]models.ObaServer{alpha, beta}, store)

	if got := trackedCountValue(t, alpha); got != 2 {
		t.Fatalf("agency count for shared server = %v, want 2", got)
	}
	if got := len(trackedAgencySeries(t)); got != 2 {
		t.Fatalf("agency info rows = %d, want 2", got)
	}
}

func TestReportTrackedAgenciesUpdatesAfterStaticSnapshotChanges(t *testing.T) {
	resetTrackedAgenciesMetrics()
	store := gtfs.NewStaticStore()
	server := models.ObaServer{ServerName: "multi", ObaBaseURL: "https://multi.example"}
	store.Set(models.ServerKey(server.ObaBaseURL, "A"), &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "A", Name: "Agency A"}}})
	reportTrackedAgencies([]models.ObaServer{server}, store)

	store.ReplaceServerSnapshot(server, map[string]*models.StaticData{
		models.ServerKey(server.ObaBaseURL, "B"): {Agencies: []remoteGtfs.Agency{{Id: "B", Name: "Agency B"}}},
	}, time.Now())
	observer := (&MetricsService{StaticStore: store}).StaticBundleObserver()
	observer(server, "B", "Agency B", &models.StaticData{Agencies: []remoteGtfs.Agency{{Id: "B", Name: "Agency B"}}})

	if got := trackedCountValue(t, server); got != 1 {
		t.Fatalf("updated agency count = %v, want 1", got)
	}
	series := trackedAgencySeries(t)
	if len(series) != 1 || series[0]["agency_id"] != "B" {
		t.Fatalf("updated inventory = %+v, want only agency B", series)
	}
}

func resetTrackedAgenciesMetrics() {
	AgenciesTrackedCount.Reset()
	AgenciesTrackedInfo.Reset()
}

func trackedAgencySeries(t *testing.T) []map[string]string {
	t.Helper()
	c := make(chan prometheus.Metric, 32)
	AgenciesTrackedInfo.Collect(c)
	close(c)

	var series []map[string]string
	for metric := range c {
		pb := &dto.Metric{}
		if err := metric.Write(pb); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		labels := make(map[string]string)
		for _, label := range pb.Label {
			labels[label.GetName()] = label.GetValue()
		}
		series = append(series, labels)
	}
	return series
}

func trackedCountValue(t *testing.T, server models.ObaServer) float64 {
	t.Helper()
	value, err := gaugeValue(AgenciesTrackedCount.WithLabelValues(server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)))
	if err != nil {
		t.Fatalf("failed reading tracked agency count: %v", err)
	}
	return value
}

func findTrackedAgency(series []map[string]string, agencyID, name, url string) (map[string]string, bool) {
	for _, labels := range series {
		if labels["agency_id"] == agencyID && labels["agency_name"] == name && labels["server_url"] == url {
			return labels, true
		}
	}
	return nil, false
}

func gaugeValue(gauge interface{ Write(*dto.Metric) error }) (float64, error) {
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		return 0, err
	}
	if metric.Gauge != nil {
		return metric.Gauge.GetValue(), nil
	}
	return 0, nil
}
