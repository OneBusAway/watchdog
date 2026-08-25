package metrics

import (
	"watchdog.onebusaway.org/internal/models"
)

// trackedAgencyKey identifies a tracked agency by its ID, name, server name,
// and base URL. The name and URL are part of the key so that label changes
// (e.g., a remote config refresh renames an agency or moves it to a new OBA
// base URL) are detected as a change rather than silently dropping out.
type trackedAgencyKey struct {
	agencyID   string
	agencyName string
	serverName string
	baseURL    string
}

// trackedAgencyKeys returns the set of (id, name, server_name, url) tuples
// currently being observed by Watchdog. It is used to detect when the
// configured server set changes so the tracked-agency metrics are only
// re-emitted on change.
func trackedAgencyKeys(servers []models.ObaServer) map[trackedAgencyKey]struct{} {
	keys := make(map[trackedAgencyKey]struct{}, len(servers))
	for _, s := range servers {
		keys[trackedAgencyKey{s.AgencyID, s.AgencyName, s.ServerName, s.ObaBaseURL}] = struct{}{}
	}
	return keys
}

// sameAgencySet reports whether two tracked agency sets are identical.
func sameAgencySet(a, b map[trackedAgencyKey]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}
