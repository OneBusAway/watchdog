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
//
// The cluster fields are populated when the stop participates in a reported
// unmatched-stop cluster, so the matching cluster series can also be cleaned up.
type trackedStop struct {
	Slug        string
	Agency      string
	StopName    string
	Lat         string
	Lon         string
	ClusterID   string
	ClusterType string
	HasCluster  bool
	LastSeen    time.Time
}

// UnmatchedStopTracker stores the most recent observation of each unmatched
// stop per server, agency, and stop ID.
//
// The outer map key is the server ID (int), the next map key is the agency ID,
// and the innermost map key is the stop ID.
type UnmatchedStopTracker struct {
	Mu      sync.RWMutex
	Entries map[int]map[string]map[string]trackedStop
}

func NewUnmatchedStopTracker() *UnmatchedStopTracker {
	return &UnmatchedStopTracker{
		Entries: make(map[int]map[string]map[string]trackedStop),
	}
}

// RecordLastSeen updates (or creates) the tracked entry for a stop that was just
// reported as unmatched. When hasCluster is true, the cluster labels are stored
// so the cluster series can be cleaned up together with the stop series.
func (t *UnmatchedStopTracker) RecordLastSeen(serverID int, slug, agency, stopID, stopName, lat, lon string, clusterID, clusterType string, hasCluster bool) {
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
	if hasCluster {
		entry.ClusterID = clusterID
		entry.ClusterType = clusterType
	} else {
		entry.ClusterID = ""
		entry.ClusterType = ""
	}
	entry.HasCluster = hasCluster

	stops[stopID] = entry
}

// ClearRoutine runs a background process that periodically removes tracked
// unmatched stops whose LastSeen timestamps exceed the given threshold,
// deleting the corresponding Prometheus gauge series.
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

// clear removes stale entries from the tracker and deletes the gauge series
// that were emitted for them.
//
// threshold: Duration after which an entry is considered stale.
func (t *UnmatchedStopTracker) clear(threshold time.Duration) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	if len(t.Entries) == 0 {
		return
	}

	now := time.Now().UTC()

	for serverID, agencies := range t.Entries {
		for agencyID, stops := range agencies {
			for stopID, entry := range stops {
				if now.Sub(entry.LastSeen) <= threshold {
					continue
				}

				if entry.HasCluster {
					UnmatchedStopClusterCount.DeleteLabelValues(entry.Slug, entry.Agency, entry.ClusterID, entry.ClusterType)
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
