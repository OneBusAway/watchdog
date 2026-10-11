package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/models"
)

func TestReportServerInfo(t *testing.T) {
	service := &MetricsService{}
	const serverURL = "https://identity-metrics.example"

	server := models.ObaServer{
		ObaBaseURL:   serverURL,
		ServiceSlug:  "metro-transit-api",
		Environment:  "production",
		Organization: "Metro Transit",
	}
	labels := map[string]string{
		"server_url":   serverURL,
		"service_slug": "metro-transit-api",
		"environment":  "production",
		"organization": "Metro Transit",
	}

	service.ReportServerInfo([]models.ObaServer{server})
	if !seriesExists(WatchdogServerInfo, prometheus.Labels(labels)) {
		t.Fatal("expected watchdog_server_info with the configured identity labels")
	}
	value, err := getMetricValue(WatchdogServerInfo, labels)
	if err != nil {
		t.Fatal(err)
	}
	if value != 1 {
		t.Fatalf("watchdog_server_info = %v, want 1", value)
	}

	// Updating identity for the same server replaces the old info series.
	server.ServiceSlug = "metro-transit-api-v2"
	service.ReportServerInfo([]models.ObaServer{server})
	if seriesExists(WatchdogServerInfo, prometheus.Labels(labels)) {
		t.Fatal("expected old identity series to be removed after a config update")
	}
	labels["service_slug"] = server.ServiceSlug
	if !seriesExists(WatchdogServerInfo, prometheus.Labels(labels)) {
		t.Fatal("expected updated identity series")
	}

	// A config from before the identity fields existed remains valid and must
	// not advertise a misleading series with blank metadata.
	legacyConfig := models.ObaServer{ObaBaseURL: serverURL}
	service.ReportServerInfo([]models.ObaServer{legacyConfig})
	if got := seriesMatching(WatchdogServerInfo, map[string]string{"server_url": serverURL}); len(got) != 0 {
		t.Fatalf("legacy config emitted %d watchdog_server_info series, want none", len(got))
	}
}
