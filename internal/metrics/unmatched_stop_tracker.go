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
	AgencyID   string
	AgencyName string
	ServerName string
	ServerURL  string
	StopName   string
	Lat        string
	Lon        string
	LastSeen   time.Time
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
	AgencyID   string
	AgencyName string
	ServerName string
	ServerURL  string
	Key        clusterKey
	LastSeen   time.Time
}

type stopKey struct {
	StopID   string
	StopName string
	Lat      string
	Lon      string
}

// UnmatchedStopTracker stores the most recent observation of each unmatched
// stop and unmatched-stop cluster per server. Stop series and cluster series
// are tracked independently: a cluster series is deleted once the cluster
// itself has not been seen for the TTL, regardless of which stops are (or were)
// its members.
//
// The outer map key is the composite server key (oba_base_url + agency_id) and
// the innermost map key is the stop identity including location (Entries) or
// the cluster key (Clusters).
type UnmatchedStopTracker struct {
	Mu       sync.RWMutex
	Entries  map[string]map[stopKey]trackedStop
	Clusters map[string]map[clusterKey]trackedCluster
}

// NewUnmatchedStopTracker creates an empty tracker. Stop entries include their
// location in the key so colliding stop IDs can be retained independently.
func NewUnmatchedStopTracker() *UnmatchedStopTracker {
	return &UnmatchedStopTracker{
		Entries:  make(map[string]map[stopKey]trackedStop),
		Clusters: make(map[string]map[clusterKey]trackedCluster),
	}
}

// RecordLastSeen updates (or creates) the tracked entry for a stop that was just
// reported as unmatched. It preserves the historical rename behavior by
// retiring another location with the same stop ID. Use RecordLocationLastSeen
// when several locations for one ID are valid at the same time.
// serverKey is the outer map key; the agencyID, agencyName, serverName, and
// serverURL are the label values of the emitted metric series.
func (t *UnmatchedStopTracker) RecordLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stopName, lat, lon string) {
	t.recordLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stopName, lat, lon, false)
}

// RecordLocationLastSeen records a location without retiring another location
// for the same stop ID. This is used when a collision produces multiple valid
// physical locations for one agency and stop ID; each location has its own
// Prometheus label set and therefore needs independent retention.
func (t *UnmatchedStopTracker) RecordLocationLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stopName, lat, lon string) {
	t.recordLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stopName, lat, lon, true)
}

// recordLastSeen contains the shared synchronized implementation for the two
// public recording modes. preserveLocations distinguishes a normal renamed
// stop from a collision where multiple physical locations must coexist.
func (t *UnmatchedStopTracker) recordLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stopName, lat, lon string, preserveLocations bool) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	stops, ok := t.Entries[serverKey]
	if !ok {
		stops = make(map[stopKey]trackedStop)
		t.Entries[serverKey] = stops
	}

	key := stopKey{StopID: stopID, StopName: stopName, Lat: lat, Lon: lon}
	if !preserveLocations {
		for oldKey, oldEntry := range stops {
			if oldKey.StopID == stopID && oldKey != key {
				ObaUnmatchedStopInfo.DeleteLabelValues(oldEntry.AgencyID, oldEntry.AgencyName, oldEntry.ServerName, oldEntry.ServerURL, oldKey.StopID, oldEntry.StopName, oldEntry.Lat, oldEntry.Lon)
				delete(stops, oldKey)
			}
		}
	}
	entry, exists := stops[key]
	if exists && (entry.AgencyName != agencyName || entry.ServerName != serverName || entry.ServerURL != serverURL || entry.AgencyID != agencyID) {
		// The stop changed its agency name, server, name, or location, so the
		// series labeled with its previous values is now stale. Delete it so
		// both it and the new series are pruned correctly, instead of freezing
		// the first-seen labels.
		ObaUnmatchedStopInfo.DeleteLabelValues(entry.AgencyID, entry.AgencyName, entry.ServerName, entry.ServerURL, stopID, entry.StopName, entry.Lat, entry.Lon)
	}

	stops[key] = trackedStop{
		AgencyID:   agencyID,
		AgencyName: agencyName,
		ServerName: serverName,
		ServerURL:  serverURL,
		StopName:   stopName,
		Lat:        lat,
		Lon:        lon,
		LastSeen:   time.Now().UTC(),
	}
}

// RecordClusterSeen updates (or creates) the tracked entry for an unmatched-stop
// cluster that was just reported. serverKey is the outer map key; the
// agencyID, agencyName, serverName, and serverURL are the label values of the
// emitted metric series.
func (t *UnmatchedStopTracker) RecordClusterSeen(serverKey, agencyID, agencyName, serverName, serverURL, stationID, clusterID, lat, lon string) {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	clusters, ok := t.Clusters[serverKey]
	if !ok {
		clusters = make(map[clusterKey]trackedCluster)
		t.Clusters[serverKey] = clusters
	}

	cluster := clusterKey{StationID: stationID, ClusterID: clusterID, Lat: lat, Lon: lon}
	entry, exists := clusters[cluster]
	if !exists {
		entry = trackedCluster{
			AgencyID:   agencyID,
			AgencyName: agencyName,
			ServerName: serverName,
			ServerURL:  serverURL,
			Key:        cluster,
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
	// Entries are keyed by stop ID plus labels/location, so each colliding
	// physical location must be expired and deleted independently.
	if len(t.Entries) == 0 {
		return
	}

	for serverKey, stops := range t.Entries {
		for key, entry := range stops {
			if now.Sub(entry.LastSeen) <= threshold {
				continue
			}

			ObaUnmatchedStopInfo.DeleteLabelValues(entry.AgencyID, entry.AgencyName, entry.ServerName, entry.ServerURL, key.StopID, entry.StopName, entry.Lat, entry.Lon)
			delete(stops, key)
		}

		if len(stops) == 0 {
			delete(t.Entries, serverKey)
		}
	}
}

func (t *UnmatchedStopTracker) clearClusters(now time.Time, threshold time.Duration) {
	if len(t.Clusters) == 0 {
		return
	}

	for serverKey, clusters := range t.Clusters {
		for key, entry := range clusters {
			if now.Sub(entry.LastSeen) <= threshold {
				continue
			}

			UnmatchedStopClusterCount.DeleteLabelValues(entry.AgencyID, entry.AgencyName, entry.ServerName, entry.ServerURL, key.StationID, key.ClusterID, key.Lat, key.Lon)
			delete(clusters, key)
		}

		if len(clusters) == 0 {
			delete(t.Clusters, serverKey)
		}
	}
}

// Prune removes every tracked stop and cluster whose server key the keep
// predicate rejects and returns the removed keys. See gtfs.StaticStore.Prune
// for why this exists.
//
// This complements ClearRoutine, which expires entries by age. A server that
// leaves the configuration stops reporting entirely, so its entries would
// otherwise sit out the full staleness threshold before going away. It does
// not delete the gauge series the entries describe: PruneStaleServers already
// retires those wholesale via DeleteSeriesForServer / DeleteSeriesForAgency,
// which covers the labels this tracker never saw as well.
func (t *UnmatchedStopTracker) Prune(keep func(serverKey string) bool) []string {
	t.Mu.Lock()
	defer t.Mu.Unlock()

	var removed []string
	for serverKey := range t.Entries {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(t.Entries, serverKey)
			delete(t.Clusters, serverKey)
		}
	}
	// A server can be present in Clusters and absent from Entries — clearStops
	// drops a stop map once it empties, while the clusters stay until their own
	// TTL — so sweep that map independently.
	for serverKey := range t.Clusters {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(t.Clusters, serverKey)
		}
	}
	return removed
}
