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
	Slug     string
	Agency   string
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
	Slug     string
	Agency   string
	Key      clusterKey
	LastSeen time.Time
}

// UnmatchedStopTracker stores the most recent observation of each unmatched
// stop and unmatched-stop cluster per server and agency. Stop series and
// cluster series are tracked independently: a cluster series is deleted once
// the cluster itself has not been seen for the TTL, regardless of which stops
// are (or were) its members.
//
// The outer map key is the server ID (int), the next map key is the agency ID,
// and the innermost map key is the stop ID (Entries) or the cluster key
// (Clusters).
type UnmatchedStopTracker struct {
	Mu       sync.RWMutex
	Entries  map[int]map[string]map[string]trackedStop
	Clusters map[int]map[string]map[clusterKey]trackedCluster
}

func NewUnmatchedStopTracker() *UnmatchedStopTracker {
	return &UnmatchedStopTracker{
		Entries:  make(map[int]map[string]map[string]trackedStop),
		Clusters: make(map[int]map[string]map[clusterKey]trackedCluster),
	}
}

// RecordLastSeen updates (or creates) the tracked entry for a stop that was just
// reported as unmatched.
func (t *UnmatchedStopTracker) RecordLastSeen(serverID int, slug, agency, stopID, stopName, lat, lon string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	agencies, ok := t.Entries[serverID]
	if !ok {
		agencies = make(map[string]map[string]trackedStop)
		t.Entries[serverID] = agencies
	}

	stops, ok := agencies[agency]
	if !ok {
		stops = make(map[string]trackedStop)
		agencies[agency] = stops
	}

	entry, exists := stops[stopID]
	if !exists {
		entry = trackedStop{
			Slug:     slug,
			Agency:   agency,
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
func (t *UnmatchedStopTracker) RecordClusterSeen(serverID int, slug, agency, stationID, clusterID, lat, lon string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	agencies, ok := t.Clusters[serverID]
	if !ok {
		agencies = make(map[string]map[clusterKey]trackedCluster)
		t.Clusters[serverID] = agencies
	}

	clusters, ok := agencies[agency]
	if !ok {
		clusters = make(map[clusterKey]trackedCluster)
		agencies[agency] = clusters
	}

	key := clusterKey{StationID: stationID, ClusterID: clusterID, Lat: lat, Lon: lon}
	entry, exists := clusters[key]
	if !exists {
		entry = trackedCluster{
			Slug:   slug,
			Agency: agency,
			Key:    key,
		}
	}

	entry.LastSeen = time.Now().UTC()

	clusters[key] = entry
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

	for serverID, agencies := range t.Entries {
		for agencyID, stops := range agencies {
			for stopID, entry := range stops {
				if now.Sub(entry.LastSeen) <= threshold {
					continue
				}

				ObaUnmatchedStopInfo.DeleteLabelValues(entry.Slug, entry.Agency, stopID, entry.StopName, entry.Lat, entry.Lon)
				delete(stops, stopID)
			}

			if len(stops) == 0 {
				delete(agencies, agencyID)
			}
		}

		if len(agencies) == 0 {
			delete(t.Entries, serverID)
		}
	}
}

func (t *UnmatchedStopTracker) clearClusters(now time.Time, threshold time.Duration) {
	if len(t.Clusters) == 0 {
		return
	}

	for serverID, agencies := range t.Clusters {
		for agencyID, clusters := range agencies {
			for key, entry := range clusters {
				if now.Sub(entry.LastSeen) <= threshold {
					continue
				}

				UnmatchedStopClusterCount.DeleteLabelValues(entry.Slug, entry.Agency, key.StationID, key.ClusterID, key.Lat, key.Lon)
				delete(clusters, key)
			}

			if len(clusters) == 0 {
				delete(agencies, agencyID)
			}
		}

		if len(agencies) == 0 {
			delete(t.Clusters, serverID)
		}
	}
}
