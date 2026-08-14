package metrics

import (
	"context"
	"sync"
	"time"
)

// trackedStop records the label values and last observation time of a stop that
// was reported as unmatched by the OBA metrics API. It is used so the gauge
// series created for the stop can be deleted (via DeleteLabelValues) after the
// stop has not been seen for a configured threshold.
type trackedStop struct {
	AgencyID string
	StopName string
	Lat      string
	Lon      string
	LastSeen time.Time
}

// clusterKey identifies a reported unmatched-stop cluster by its station, S2
// cluster, and reported coordinates. All four are needed because the series is
// labeled by the same values and DeleteLabelValues must match exactly.
type clusterKey struct {
	StationID string
	ClusterID string
	Lat       string
	Lon       string
}

// trackedCluster records the last observation time of an unmatched-stop
// cluster, so the cluster gauge series can be deleted after the cluster has
// not been seen for a configured threshold.
type trackedCluster struct {
	AgencyID string
	Key      clusterKey
	LastSeen time.Time
}

// UnmatchedStopTracker stores the most recent observation of each unmatched
// stop and unmatched-stop cluster per agency. Stop series and cluster series
// are tracked independently: a cluster series is deleted once the cluster
// itself has not been seen for the TTL, regardless of which stops are (or were)
// its members.
//
// The outer map key is the agency ID and the innermost map key is the stop ID
// (Entries) or the cluster key (Clusters).
type UnmatchedStopTracker struct {
	Mu       sync.RWMutex
	Entries  map[string]map[string]trackedStop
	Clusters map[string]map[clusterKey]trackedCluster
}

func NewUnmatchedStopTracker() *UnmatchedStopTracker {
	return &UnmatchedStopTracker{
		Entries:  make(map[string]map[string]trackedStop),
		Clusters: make(map[string]map[clusterKey]trackedCluster),
	}
}

// RecordLastSeen updates (or creates) the tracked entry for a stop that was just
// reported as unmatched.
func (t *UnmatchedStopTracker) RecordLastSeen(agencyID, stopID, stopName, lat, lon string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	stops, ok := t.Entries[agencyID]
	if !ok {
		stops = make(map[string]trackedStop)
		t.Entries[agencyID] = stops
	}

	entry, exists := stops[stopID]
	if !exists {
		entry = trackedStop{
			AgencyID: agencyID,
			StopName: stopName,
			Lat:      lat,
			Lon:      lon,
		}
	}

	entry.LastSeen = time.Now().UTC()

	stops[stopID] = entry
}

// RecordClusterSeen updates (or creates) the tracked entry for an unmatched-stop
// cluster that was just reported.
func (t *UnmatchedStopTracker) RecordClusterSeen(agencyID, stationID, clusterID, lat, lon string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	clusters, ok := t.Clusters[agencyID]
	if !ok {
		clusters = make(map[clusterKey]trackedCluster)
		t.Clusters[agencyID] = clusters
	}

	cluster := clusterKey{StationID: stationID, ClusterID: clusterID, Lat: lat, Lon: lon}
	entry, exists := clusters[cluster]
	if !exists {
		entry = trackedCluster{
			AgencyID: agencyID,
			Key:      cluster,
		}
	}

	entry.LastSeen = time.Now().UTC()

	clusters[cluster] = entry
}

// ClearRoutine runs a background process that periodically removes tracked
// unmatched stops and clusters whose LastSeen timestamps exceed the given
// threshold, deleting the corresponding Prometheus gauge series.
//
// ctx: Context for canceling the routine.
// timeInterval: Interval at which cleanup checks are performed.
// threshold: Duration after which an entry is considered stale and removed.
func (t *UnmatchedStopTracker) ClearRoutine(ctx context.Context, timeInterval, threshold time.Duration) {
	ticker := time.NewTicker(timeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.clear(threshold)
		case <-ctx.Done():
			return
		}
	}
}

// clear removes stale stop and cluster entries from the tracker and deletes
// the gauge series that were emitted for them.
//
// threshold: Duration after which an entry is considered stale.
func (t *UnmatchedStopTracker) clear(threshold time.Duration) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	now := time.Now().UTC()

	t.clearStops(now, threshold)
	t.clearClusters(now, threshold)
}

func (t *UnmatchedStopTracker) clearStops(now time.Time, threshold time.Duration) {
	if len(t.Entries) == 0 {
		return
	}

	for agencyID, stops := range t.Entries {
		for stopID, entry := range stops {
			if now.Sub(entry.LastSeen) <= threshold {
				continue
			}

			ObaUnmatchedStopInfo.DeleteLabelValues(entry.AgencyID, stopID, entry.StopName, entry.Lat, entry.Lon)
			delete(stops, stopID)
		}

		if len(stops) == 0 {
			delete(t.Entries, agencyID)
		}
	}
}

func (t *UnmatchedStopTracker) clearClusters(now time.Time, threshold time.Duration) {
	if len(t.Clusters) == 0 {
		return
	}

	for agencyID, clusters := range t.Clusters {
		for key, entry := range clusters {
			if now.Sub(entry.LastSeen) <= threshold {
				continue
			}

			UnmatchedStopClusterCount.DeleteLabelValues(entry.AgencyID, key.StationID, key.ClusterID, key.Lat, key.Lon)
			delete(clusters, key)
		}

		if len(clusters) == 0 {
			delete(t.Clusters, agencyID)
		}
	}
}
