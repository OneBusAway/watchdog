package gtfs

import (
	"sync"
)

// RouteAgencyIndex is the attribution index for one static snapshot. It maps
// both route_id and trip_id to their owning agency and keeps agency names next
// to those maps so replacement is atomic. Entries are keyed by the composite
// models.ServerKey identity; agency-mode entries therefore remain independent
// even when they share a base URL.
type RouteAgencyIndex struct {
	mu       sync.RWMutex
	byServer map[string]*serverIndex
}

type serverIndex struct {
	routeIDs    map[string]string
	tripIDs     map[string]string
	agencyNames map[string]string
}

func NewRouteAgencyIndex() *RouteAgencyIndex {
	return &RouteAgencyIndex{byServer: make(map[string]*serverIndex)}
}

// Replace atomically publishes all attribution maps for a static snapshot.
func (idx *RouteAgencyIndex) Replace(serverKey string, routes, trips, agencyNames map[string]string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.byServer == nil {
		idx.byServer = make(map[string]*serverIndex)
	}
	idx.byServer[serverKey] = &serverIndex{
		routeIDs:    cloneStringMap(routes),
		tripIDs:     cloneStringMap(trips),
		agencyNames: cloneStringMap(agencyNames),
	}
}

func (idx *RouteAgencyIndex) Get(serverKey, routeID string) (string, bool) {
	if routeID == "" {
		return "", false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return "", false
	}
	agencyID, ok := si.routeIDs[routeID]
	return agencyID, ok && agencyID != ""
}

func (idx *RouteAgencyIndex) GetTrip(serverKey, tripID string) (string, bool) {
	if tripID == "" {
		return "", false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return "", false
	}
	agencyID, ok := si.tripIDs[tripID]
	return agencyID, ok && agencyID != ""
}

// ResolveVehicleAgency applies both identifiers. A disagreement is treated as
// unresolved rather than silently preferring one source field.
func (idx *RouteAgencyIndex) ResolveVehicleAgency(serverKey, routeID, tripID string) (string, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return "", false
	}
	routeAgency, routeOK := si.routeIDs[routeID]
	tripAgency, tripOK := si.tripIDs[tripID]
	if routeOK && routeAgency == "" {
		routeOK = false
	}
	if tripOK && tripAgency == "" {
		tripOK = false
	}
	switch {
	case routeOK && tripOK && routeAgency != tripAgency:
		return "", false
	case routeOK:
		return routeAgency, true
	case tripOK:
		return tripAgency, true
	default:
		return "", false
	}
}

// Has reports whether an attribution snapshot exists for serverKey, even when
// it contains no resolvable routes or trips.
func (idx *RouteAgencyIndex) Has(serverKey string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.lookupLocked(serverKey) != nil
}

func (idx *RouteAgencyIndex) AgencyNameFor(serverKey, agencyID string) (string, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return "", false
	}
	name, ok := si.agencyNames[agencyID]
	return name, ok
}

func (idx *RouteAgencyIndex) RangeServerKeys(fn func(serverKey string) bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for key := range idx.byServer {
		if !fn(key) {
			return
		}
	}
}

func (idx *RouteAgencyIndex) Clear(serverKey string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.byServer, serverKey)
}

func (idx *RouteAgencyIndex) PruneServers(keep func(serverKey string) bool) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	var removed []string
	for serverKey := range idx.byServer {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(idx.byServer, serverKey)
		}
	}
	return removed
}

func (idx *RouteAgencyIndex) lookupLocked(serverKey string) *serverIndex {
	return idx.byServer[serverKey]
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
