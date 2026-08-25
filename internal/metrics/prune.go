package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// vectorDeleter is the slice of the prometheus *MetricVec API this package
// needs in order to retire series: every Vec type (Gauge, Counter, Histogram)
// implements DeletePartialMatch.
type vectorDeleter interface {
	DeletePartialMatch(prometheus.Labels) int
}

// trackedVectors holds every metric vector declared in this package.
//
// Pruning the in-memory stores when a server leaves the config is only half
// the job: the Prometheus series themselves would otherwise sit at their last
// value forever, so /metrics keeps advertising servers Watchdog no longer
// monitors. DeleteSeriesForServer walks this slice to retire them.
//
// The slice is populated by tracked() at package-variable initialization, so
// there is no second list of vector names to keep in sync. Coverage is not
// automatic, though: a new vector declared WITHOUT the tracked(...) wrapper is
// silently never retired, which would quietly undo the pruning this file
// exists to provide. TestEveryMetricVectorIsTracked guards that.
var trackedVectors []vectorDeleter

// tracked registers a metric vector for pruning and returns it unchanged, so
// declarations read as `X = tracked(promauto.NewGaugeVec(...))`.
func tracked[T vectorDeleter](vec T) T {
	trackedVectors = append(trackedVectors, vec)
	return vec
}

// DeleteSeriesForServer retires every series labelled with the given
// (sanitized) server_url, across every metric this package declares. Call it
// when a server is removed from the configuration.
//
// Vectors that carry no server_url label simply match nothing. Returns the
// number of series deleted.
func DeleteSeriesForServer(serverURL string) int {
	return deleteMatching(prometheus.Labels{"server_url": serverURL})
}

// DeleteSeriesForAgency retires the series belonging to a single agency on a
// server that is itself still configured — the case where an agency-scoped
// entry disappears but other entries keep the same oba_base_url.
//
// Series that carry a server_url but no agency_id (the server-scoped ones,
// e.g. oba_api_status) are deliberately left alone: the server is still being
// monitored. Returns the number of series deleted.
func DeleteSeriesForAgency(serverURL, agencyID string) int {
	return deleteMatching(prometheus.Labels{"server_url": serverURL, "agency_id": agencyID})
}

// DeleteSeriesForServerScope retires the series a server-scoped entry emits
// that belong to no agency: the agency_id="" catch-all that carries vehicles
// the route index could not attribute, plus the server-scoped
// gtfs_rt_unattributed_vehicles_count, which has no agency_id label at all and
// so cannot be reached by a match on that label.
//
// This is the conversion case: an operator replaces a server-scoped entry with
// agency-scoped entries on the same oba_base_url. The URL is still configured,
// so the server never counts as departed, but nothing writes those series any
// more and they would otherwise sit frozen at their final server-mode values —
// including gtfs_rt_unattributed_vehicles_count, the gauge operators are told
// to alert on for static-feed coverage.
func DeleteSeriesForServerScope(serverURL string) int {
	deleted := deleteMatching(prometheus.Labels{"server_url": serverURL, "agency_id": ""})
	deleted += GtfsRtUnattributedVehicles.DeletePartialMatch(prometheus.Labels{"server_url": serverURL})
	return deleted
}

func deleteMatching(labels prometheus.Labels) int {
	deleted := 0
	for _, vec := range trackedVectors {
		deleted += vec.DeletePartialMatch(labels)
	}
	return deleted
}
