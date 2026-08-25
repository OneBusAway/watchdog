package gtfs

import "sync"

// RouteAgencyIndex maps every route_id known to a server to the agency_id that
// owns it. It is the single source of truth for attributing GTFS-RT vehicles
// to agencies: when an RT feed reports a vehicle, we look at
// vehicle.Trip.RouteID, ask the index which agency owns that route, and only
// then emit per-agency metrics for it.
//
// The index is built in one place (during static-feed parse) and read by every
// RT-metric function on every 30-second scrape. Because the build is bounded
// (once per static download, every 24h) and the reads are O(1) map lookups,
// the index has no per-scrape cost beyond the lookup itself.
//
// Concurrency: the index uses sync.RWMutex. Writes (Set / Clear / SetAgencyName)
// take the write lock; reads (Get / AgencyNameFor / Range) take the read lock.
// A scrape that races a 24h refresh sees a stale index for at most one tick
// (~30s); the next scrape sees the new state. No corruption, no torn reads.
type RouteAgencyIndex struct {
	mu       sync.RWMutex
	byServer map[string]*serverIndex
}

// serverIndex is the per-server state. routeIDs is the canonical route_id ->
// agency_id map. agencyNames is a reverse index built lazily so dashboards can
// recover the human-friendly agency_name without re-parsing the static bundle
// — useful when only the agency_id is known (e.g. from /metrics.json) and the
// collector wants to label metrics consistently.
type serverIndex struct {
	routeIDs    map[string]string // route_id -> agency_id
	agencyNames map[string]string // agency_id -> agency_name
}

// NewRouteAgencyIndex returns an empty RouteAgencyIndex.
func NewRouteAgencyIndex() *RouteAgencyIndex {
	return &RouteAgencyIndex{
		byServer: make(map[string]*serverIndex),
	}
}

// Set replaces the route → agency map for a single server. Called once per
// static-feed parse: the caller hands in a map covering every route declared
// in every feed attributed to that server.
//
// baseURL is the OBA server's base URL, stored verbatim — it is NOT sanitized
// here. Every reader (Get, AgencyNameFor) must therefore pass the same raw
// oba_base_url the writer used, or attribution fails silently. Callers that
// hold a sanitized URL must not mix the two forms.
func (idx *RouteAgencyIndex) Set(baseURL string, routes map[string]string) {
	if routes == nil {
		routes = map[string]string{}
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.byServer == nil {
		idx.byServer = make(map[string]*serverIndex)
	}
	idx.byServer[baseURL] = &serverIndex{
		routeIDs:    routes,
		agencyNames: map[string]string{},
	}
}

// SetAgencyName records the human-readable name for one agency on a server.
// Called during static-feed parse: the parser knows the agency_id and the
// agency_name from agency.txt, so it can populate this side index without
// scanning the route map.
func (idx *RouteAgencyIndex) SetAgencyName(baseURL, agencyID, agencyName string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	si, ok := idx.byServer[baseURL]
	if !ok {
		si = &serverIndex{routeIDs: map[string]string{}, agencyNames: map[string]string{}}
		idx.byServer[baseURL] = si
	}
	si.agencyNames[agencyID] = agencyName
}

// Get returns the agency_id that owns the given route_id on the given server.
// The second return value is false when the route is unknown to this server,
// which signals to callers that the vehicle cannot be attributed (its
// TripDescriptor.route_id is empty, references a route we don't have static
// data for, or the index hasn't been populated yet for this server).
func (idx *RouteAgencyIndex) Get(baseURL, routeID string) (string, bool) {
	if routeID == "" {
		return "", false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si, ok := idx.byServer[baseURL]
	if !ok {
		return "", false
	}
	agencyID, ok := si.routeIDs[routeID]
	return agencyID, ok
}

// AgencyNameFor returns the agency_name recorded for an agency_id on a server.
// Returns ("", false) if no name is known. The collector uses this to populate
// the agency_name metric label when only the agency_id is known (e.g., when
// /metrics.json reports a new agency we haven't seen yet).
func (idx *RouteAgencyIndex) AgencyNameFor(baseURL, agencyID string) (string, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si, ok := idx.byServer[baseURL]
	if !ok {
		return "", false
	}
	name, ok := si.agencyNames[agencyID]
	return name, ok
}

// RangeServerKeys invokes fn for every baseURL currently indexed. Stops early
// if fn returns false. Useful for tests and for any caller that needs to
// enumerate the servers the index knows about.
func (idx *RouteAgencyIndex) RangeServerKeys(fn func(baseURL string) bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for k := range idx.byServer {
		if !fn(k) {
			return
		}
	}
}

// Clear removes all entries for a single server. Currently unused by the
// collection loop but exported so tests and admin tooling can reset state.
func (idx *RouteAgencyIndex) Clear(baseURL string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.byServer, baseURL)
}

// PruneServers removes every server whose base URL the keep predicate rejects
// and returns the removed base URLs.
//
// Unlike the other stores this index is keyed by the raw oba_base_url the
// writer used, not by a serverKey, so callers must sanitize on their side of
// the predicate rather than expecting a serverKey here.
func (idx *RouteAgencyIndex) PruneServers(keep func(baseURL string) bool) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var removed []string
	for baseURL := range idx.byServer {
		if !keep(baseURL) {
			removed = append(removed, baseURL)
			delete(idx.byServer, baseURL)
		}
	}
	return removed
}
