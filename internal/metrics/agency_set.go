package metrics

import (
	"watchdog.onebusaway.org/internal/models"
)

// trackedAgencyIDs returns the set of agency IDs currently being observed by
// Watchdog. It is used to detect when the configured server set changes so the
// tracked-agency metrics are only re-emitted on change.
func trackedAgencyIDs(servers []models.ObaServer) map[string]struct{} {
	ids := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		ids[s.AgencyID] = struct{}{}
	}
	return ids
}

// sameAgencySet reports whether two agency ID sets are identical.
func sameAgencySet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
