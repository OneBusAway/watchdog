package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestDeleteSeriesForServerRetiresEverySeries covers the other half of
// pruning: dropping a server's entries from the in-memory stores leaves its
// Prometheus series sitting at their last value forever, so /metrics keeps
// advertising a server nobody monitors any more.
func TestDeleteSeriesForServerRetiresEverySeries(t *testing.T) {
	const goneURL = "https://gone.example.com"
	const keptURL = "https://kept.example.com"

	RealtimeVehiclePositions.WithLabelValues("agency-a", "Agency A", "gone", goneURL).Set(7)
	ObaApiStatus.WithLabelValues("gone", goneURL).Set(1)
	VehicleReportCount.WithLabelValues("va", "agency-a", "Agency A", "gone", goneURL, "0").Inc()
	RealtimeVehiclePositions.WithLabelValues("agency-b", "Agency B", "kept", keptURL).Set(3)

	if deleted := DeleteSeriesForServer(goneURL); deleted < 3 {
		t.Fatalf("expected at least the 3 seeded series to be deleted, got %d", deleted)
	}

	for _, tc := range []struct {
		name   string
		vec    prometheus.Collector
		labels prometheus.Labels
	}{
		{"realtime positions", RealtimeVehiclePositions, prometheus.Labels{"agency_id": "agency-a", "agency_name": "Agency A", "server_name": "gone", "server_url": goneURL}},
		{"api status", ObaApiStatus, prometheus.Labels{"server_name": "gone", "server_url": goneURL}},
		{"report count", VehicleReportCount, prometheus.Labels{"vehicle_id": "va", "agency_id": "agency-a", "agency_name": "Agency A", "server_name": "gone", "server_url": goneURL, "feed": "0"}},
	} {
		if seriesExists(tc.vec, tc.labels) {
			t.Fatalf("expected the %s series for the removed server to be gone", tc.name)
		}
	}

	if !seriesExists(RealtimeVehiclePositions, prometheus.Labels{"agency_id": "agency-b", "agency_name": "Agency B", "server_name": "kept", "server_url": keptURL}) {
		t.Fatal("expected the still-configured server's series to survive")
	}
}

// TestDeleteSeriesForAgencyLeavesServerScopedSeries checks the narrower case:
// one agency leaves a server that is still configured, so its agency-labelled
// series go but the server's own series stay.
func TestDeleteSeriesForAgencyLeavesServerScopedSeries(t *testing.T) {
	const url = "https://shared.example.com"

	RealtimeVehiclePositions.WithLabelValues("agency-x", "Agency X", "shared", url).Set(1)
	RealtimeVehiclePositions.WithLabelValues("agency-y", "Agency Y", "shared", url).Set(2)
	ObaApiStatus.WithLabelValues("shared", url).Set(1)

	DeleteSeriesForAgency(url, "agency-x")

	if seriesExists(RealtimeVehiclePositions, prometheus.Labels{"agency_id": "agency-x", "agency_name": "Agency X", "server_name": "shared", "server_url": url}) {
		t.Fatal("expected the departed agency's series to be deleted")
	}
	if !seriesExists(RealtimeVehiclePositions, prometheus.Labels{"agency_id": "agency-y", "agency_name": "Agency Y", "server_name": "shared", "server_url": url}) {
		t.Fatal("expected the remaining agency's series to survive")
	}
	if !seriesExists(ObaApiStatus, prometheus.Labels{"server_name": "shared", "server_url": url}) {
		t.Fatal("expected the server-scoped series to survive: the server is still configured")
	}
}

// seriesExists reports whether a collector currently exposes a series with
// exactly the given labels, without creating it as a side effect.
func seriesExists(collector prometheus.Collector, want prometheus.Labels) bool {
	ch := make(chan prometheus.Metric)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	found := false
	for m := range ch {
		if found {
			continue // drain, so Collect never blocks
		}
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			continue
		}
		labels := make(prometheus.Labels, len(pb.Label))
		for _, l := range pb.Label {
			labels[l.GetName()] = l.GetValue()
		}
		if len(labels) != len(want) {
			continue
		}
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			found = true
		}
	}
	return found
}
