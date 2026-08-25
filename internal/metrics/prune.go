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
// a vector added to metrics.go is covered automatically — there is no second
// list to keep in sync.
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
	if agencyID == "" {
		return 0
	}
	return deleteMatching(prometheus.Labels{"server_url": serverURL, "agency_id": agencyID})
}

func deleteMatching(labels prometheus.Labels) int {
	deleted := 0
	for _, vec := range trackedVectors {
		deleted += vec.DeletePartialMatch(labels)
	}
	return deleted
}
