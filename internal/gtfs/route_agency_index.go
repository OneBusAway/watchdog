package gtfs

import (
	"sort"
	"sync"
)

type AttributionFailureReason string

const (
	AttributionMissingVehicleID       AttributionFailureReason = "missing_vehicle_id"
	AttributionMissingRouteID         AttributionFailureReason = "missing_route_id"
	AttributionUnknownRouteID         AttributionFailureReason = "unknown_route_id"
	AttributionAgencyNotReportedByOBA AttributionFailureReason = "agency_not_reported_by_oba"
	AttributionAmbiguousIdentifier    AttributionFailureReason = "ambiguous_identifier"
	AttributionRouteTripConflict      AttributionFailureReason = "route_trip_conflict"
)

var AttributionFailureReasons = []AttributionFailureReason{
	AttributionMissingVehicleID,
	AttributionMissingRouteID,
	AttributionUnknownRouteID,
	AttributionAgencyNotReportedByOBA,
	AttributionAmbiguousIdentifier,
	AttributionRouteTripConflict,
}

type AttributionResult struct {
	AgencyID   string
	Candidates []string
	Reason     AttributionFailureReason
}

func (r AttributionResult) Resolved() bool { return r.AgencyID != "" && r.Reason == "" }

type VehicleAttributionResolver interface {
	ResolveVehicleAttribution(serverKey, routeID, tripID string) AttributionResult
	AgencyNames(serverKey string) map[string]string
}

type AttributionSnapshot struct {
	serverKey string
	index     *serverIndex
}

// RouteAgencyIndex is the attribution index for one static snapshot. It maps
// route_id and trip_id to candidate owning agencies and keeps agency names next
// to those maps so replacement is atomic. Entries are keyed by the composite
// models.ServerKey identity; agency-mode entries therefore remain independent
// even when they share a base URL.
type RouteAgencyIndex struct {
	mu       sync.RWMutex
	byServer map[string]*serverIndex
}

type serverIndex struct {
	routeIDs    map[string][]string
	tripIDs     map[string][]string
	agencyNames map[string]string
	// agencies is every non-empty agency_id the route and trip maps attribute
	// to, precomputed so per-tick callers need not scan the maps.
	agencies map[string]struct{}
}

func NewRouteAgencyIndex() *RouteAgencyIndex {
	return &RouteAgencyIndex{byServer: make(map[string]*serverIndex)}
}

// Replace atomically publishes all attribution maps for a static snapshot.
func (idx *RouteAgencyIndex) Replace(serverKey string, routes, trips, agencyNames map[string]string) {
	idx.ReplaceCandidates(serverKey, singletonCandidates(routes), singletonCandidates(trips), agencyNames)
}

func (idx *RouteAgencyIndex) ReplaceCandidates(serverKey string, routes, trips map[string][]string, agencyNames map[string]string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.byServer == nil {
		idx.byServer = make(map[string]*serverIndex)
	}
	idx.byServer[serverKey] = &serverIndex{
		routeIDs:    cloneCandidates(routes),
		tripIDs:     cloneCandidates(trips),
		agencyNames: cloneStringMap(agencyNames),
		agencies:    attributedAgencies(routes, trips),
	}
}

func attributedAgencies(routes, trips map[string][]string) map[string]struct{} {
	agencies := make(map[string]struct{})
	for _, m := range []map[string][]string{routes, trips} {
		for _, candidates := range m {
			for _, agencyID := range candidates {
				if agencyID != "" {
					agencies[agencyID] = struct{}{}
				}
			}
		}
	}
	return agencies
}

// AttributesOnlyTo reports whether the snapshot for serverKey attributes no
// route or trip to any agency other than agencyID. It is false when no
// snapshot exists.
func (idx *RouteAgencyIndex) AttributesOnlyTo(serverKey, agencyID string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return attributesOnlyTo(idx.lookupLocked(serverKey), agencyID)
}

func attributesOnlyTo(si *serverIndex, agencyID string) bool {
	if si == nil {
		return false
	}
	for attributed := range si.agencies {
		if attributed != agencyID {
			return false
		}
	}
	return true
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
	return singletonCandidate(si.routeIDs[routeID])
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
	return singletonCandidate(si.tripIDs[tripID])
}

// ResolveVehicleAgency applies both identifiers. A disagreement is treated as
// unresolved rather than silently preferring one source field.
func (idx *RouteAgencyIndex) ResolveVehicleAgency(serverKey, routeID, tripID string) (string, bool) {
	result := idx.ResolveVehicleAttribution(serverKey, routeID, tripID)
	return result.AgencyID, result.Resolved()
}

func (idx *RouteAgencyIndex) ResolveVehicleAttribution(serverKey, routeID, tripID string) AttributionResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return resolveVehicleAttribution(idx.lookupLocked(serverKey), routeID, tripID)
}

func resolveVehicleAttribution(si *serverIndex, routeID, tripID string) AttributionResult {
	if si == nil {
		return unknownAttribution(routeID)
	}
	routeCandidates := si.routeIDs[routeID]
	tripCandidates := si.tripIDs[tripID]
	if len(routeCandidates) > 0 && len(tripCandidates) > 0 {
		common := intersectCandidates(routeCandidates, tripCandidates)
		switch len(common) {
		case 1:
			return AttributionResult{AgencyID: common[0], Candidates: common}
		case 0:
			return AttributionResult{Candidates: unionCandidates(routeCandidates, tripCandidates), Reason: AttributionRouteTripConflict}
		default:
			return AttributionResult{Candidates: common, Reason: AttributionAmbiguousIdentifier}
		}
	}
	candidates := routeCandidates
	if len(candidates) == 0 {
		candidates = tripCandidates
	}
	if len(candidates) == 1 {
		return AttributionResult{AgencyID: candidates[0], Candidates: append([]string(nil), candidates...)}
	}
	if len(candidates) > 1 {
		return AttributionResult{Candidates: append([]string(nil), candidates...), Reason: AttributionAmbiguousIdentifier}
	}
	return unknownAttribution(routeID)
}

func (idx *RouteAgencyIndex) Snapshot(serverKey string) *AttributionSnapshot {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return &AttributionSnapshot{serverKey: serverKey}
	}
	// A serverIndex is never mutated after ReplaceCandidates publishes it (a
	// replacement installs a new pointer), so the snapshot can share it rather
	// than deep-copying every route and trip map on each collection tick.
	return &AttributionSnapshot{serverKey: serverKey, index: si}
}

// AttributesOnlyTo is the snapshot counterpart of RouteAgencyIndex.AttributesOnlyTo.
func (snapshot *AttributionSnapshot) AttributesOnlyTo(serverKey, agencyID string) bool {
	if snapshot == nil || serverKey != snapshot.serverKey {
		return false
	}
	return attributesOnlyTo(snapshot.index, agencyID)
}

func (snapshot *AttributionSnapshot) ResolveVehicleAttribution(serverKey, routeID, tripID string) AttributionResult {
	if snapshot == nil || serverKey != snapshot.serverKey {
		return unknownAttribution(routeID)
	}
	return resolveVehicleAttribution(snapshot.index, routeID, tripID)
}

func (snapshot *AttributionSnapshot) AgencyNames(serverKey string) map[string]string {
	if snapshot == nil || serverKey != snapshot.serverKey || snapshot.index == nil {
		return nil
	}
	return cloneStringMap(snapshot.index.agencyNames)
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

func (idx *RouteAgencyIndex) AgencyNames(serverKey string) map[string]string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	si := idx.lookupLocked(serverKey)
	if si == nil {
		return nil
	}
	return cloneStringMap(si.agencyNames)
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

func singletonCandidates(values map[string]string) map[string][]string {
	result := make(map[string][]string, len(values))
	for identifier, agencyID := range values {
		if agencyID != "" {
			result[identifier] = []string{agencyID}
		}
	}
	return result
}

func cloneCandidates(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for identifier, candidates := range in {
		copyOfCandidates := append([]string(nil), candidates...)
		sort.Strings(copyOfCandidates)
		out[identifier] = compactStrings(copyOfCandidates)
	}
	return out
}

func singletonCandidate(candidates []string) (string, bool) {
	if len(candidates) != 1 || candidates[0] == "" {
		return "", false
	}
	return candidates[0], true
}

func unknownAttribution(routeID string) AttributionResult {
	if routeID == "" {
		return AttributionResult{Reason: AttributionMissingRouteID}
	}
	return AttributionResult{Reason: AttributionUnknownRouteID}
}

func intersectCandidates(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, candidate := range right {
		rightSet[candidate] = struct{}{}
	}
	result := make([]string, 0, min(len(left), len(right)))
	for _, candidate := range left {
		if _, ok := rightSet[candidate]; ok {
			result = append(result, candidate)
		}
	}
	sort.Strings(result)
	return compactStrings(result)
}

func unionCandidates(left, right []string) []string {
	result := append(append([]string(nil), left...), right...)
	sort.Strings(result)
	return compactStrings(result)
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}
