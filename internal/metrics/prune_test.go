package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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

// seriesExists reports whether a collector currently exposes a series carrying
// exactly the given labels. Exact rather than subset matching, because these
// assertions name the whole label set of the series they mean.
func seriesExists(collector prometheus.Collector, want prometheus.Labels) bool {
	for _, pb := range seriesMatching(collector, want) {
		if len(pb.Label) == len(want) {
			return true
		}
	}
	return false
}

// TestEveryMetricVectorIsTracked guards the one invariant in this package
// whose violation is silent and would undo pruning entirely: a metric vector
// declared without the tracked(...) wrapper never gets its series retired, so a
// departed server keeps advertising it on /metrics forever.
//
// It parses metrics.go rather than counting, so the failure message names the
// offending vector instead of just reporting that a number changed.
func TestEveryMetricVectorIsTracked(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "metrics.go", nil, 0)
	if err != nil {
		t.Fatalf("parse metrics.go: %v", err)
	}

	var untracked []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, value := range spec.Values {
			name := "<unnamed>"
			if i < len(spec.Names) {
				name = spec.Names[i].Name
			}
			if isVectorConstructor(value) {
				untracked = append(untracked, name)
			}
		}
		return true
	})

	if len(untracked) > 0 {
		t.Errorf("these metric vectors are not wrapped in tracked(...), so their series "+
			"would never be retired when a server leaves the config: %v", untracked)
	}
}

// isVectorConstructor reports whether expr is a bare promauto.NewXxxVec(...)
// call — that is, one that is NOT wrapped in tracked(...). A wrapped
// declaration presents as tracked(...) at the top level, so its inner call is
// never inspected here.
func isVectorConstructor(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "promauto" {
		return false
	}
	return strings.HasPrefix(sel.Sel.Name, "New") && strings.HasSuffix(sel.Sel.Name, "Vec")
}
