package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"watchdog.onebusaway.org/internal/models"
)

func TestReportTrackedAgencies(t *testing.T) {
	resetTrackedAgenciesState()

	servers := []models.ObaServer{
		{AgencyName: "Alpha", AgencyID: "agency-a", ObaBaseURL: "https://alpha.example.com"},
		{AgencyName: "Beta", AgencyID: "agency-b", ObaBaseURL: "https://beta.example.com"},
	}

	reportTrackedAgencies(servers)

	count, err := gaugeValue(AgenciesTrackedCount)
	if err != nil {
		t.Fatalf("Failed to read AgenciesTrackedCount: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected tracked agencies count to be 2, got %v", count)
	}

	series := trackedAgencySeries(t)
	for _, s := range servers {
		if _, ok := findTrackedAgency(series, s.AgencyID, s.AgencyName, s.ObaBaseURL); !ok {
			t.Errorf("Expected tracked agency series for %s, got %+v", s.AgencyID, series)
		}
	}
}

func TestReportTrackedAgenciesReflectsRemovedAgencies(t *testing.T) {
	resetTrackedAgenciesState()

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Alpha", AgencyID: "agency-a", ObaBaseURL: "https://alpha.example.com"},
		{AgencyName: "Beta", AgencyID: "agency-b", ObaBaseURL: "https://beta.example.com"},
	})

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Beta", AgencyID: "agency-b", ObaBaseURL: "https://beta.example.com"},
	})

	count, err := gaugeValue(AgenciesTrackedCount)
	if err != nil {
		t.Fatalf("Failed to read AgenciesTrackedCount: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected tracked agencies count to be 1 after removal, got %v", count)
	}

	series := trackedAgencySeries(t)
	if _, ok := findTrackedAgency(series, "agency-a", "Alpha", "https://alpha.example.com"); ok {
		t.Errorf("Expected removed agency series to be gone, got %+v", series)
	}
	if _, ok := findTrackedAgency(series, "agency-b", "Beta", "https://beta.example.com"); !ok {
		t.Errorf("Expected remaining agency series for %q, got %+v", "agency-b", series)
	}
}

func TestReportTrackedAgenciesSkipsUnchangedSet(t *testing.T) {
	resetTrackedAgenciesState()

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Alpha", AgencyID: "agency-a", ObaBaseURL: "https://alpha.example.com"},
	})

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Alpha", AgencyID: "agency-a", ObaBaseURL: "https://alpha.example.com"},
	})

	series := trackedAgencySeries(t)
	if _, ok := findTrackedAgency(series, "agency-a", "Alpha", "https://alpha.example.com"); !ok {
		t.Errorf("Expected unchanged set to be a no-op, got %+v", series)
	}
	if len(series) != 1 {
		t.Errorf("Expected exactly one series after no-op re-report, got %+v", series)
	}
}

func TestReportTrackedAgenciesPropagatesLabelChanges(t *testing.T) {
	resetTrackedAgenciesState()

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Alpha", AgencyID: "agency-a", ObaBaseURL: "https://alpha.example.com"},
	})

	reportTrackedAgencies([]models.ObaServer{
		{AgencyName: "Alpha Renamed", AgencyID: "agency-a", ObaBaseURL: "https://renamed.example.com"},
	})

	series := trackedAgencySeries(t)
	if _, ok := findTrackedAgency(series, "agency-a", "Alpha Renamed", "https://renamed.example.com"); !ok {
		t.Errorf("Expected renamed agency series to be re-emitted, got %+v", series)
	}
	if _, ok := findTrackedAgency(series, "agency-a", "Alpha", "https://alpha.example.com"); ok {
		t.Errorf("Expected stale series to be gone after rename, got %+v", series)
	}
}

// resetTrackedAgenciesState clears the package-level change-detection state so
// tests start from a clean slate.
func resetTrackedAgenciesState() {
	lastTrackedAgencies = nil
	trackedAgenciesReported = false
	AgenciesTrackedInfo.Reset()
}

// trackedAgencySeries gathers the label sets currently exposed by
// AgenciesTrackedInfo.
func trackedAgencySeries(t *testing.T) []map[string]string {
	t.Helper()
	c := make(chan prometheus.Metric, 32)
	AgenciesTrackedInfo.Collect(c)
	close(c)

	var series []map[string]string
	for m := range c {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		labels := make(map[string]string)
		for _, lp := range pb.Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		series = append(series, labels)
	}
	return series
}

func findTrackedAgency(series []map[string]string, agencyID, name, url string) (map[string]string, bool) {
	for _, labels := range series {
		if labels["agency_id"] == agencyID && labels["agency_name"] == name && labels["server_url"] == url {
			return labels, true
		}
	}
	return nil, false
}

// gaugeValue reads the current float64 value of a prometheus Gauge.
func gaugeValue(gauge interface{ Write(*dto.Metric) error }) (float64, error) {
	pb := &dto.Metric{}
	if err := gauge.Write(pb); err != nil {
		return 0, err
	}
	if pb.Gauge != nil {
		return pb.Gauge.GetValue(), nil
	}
	return 0, nil
}
