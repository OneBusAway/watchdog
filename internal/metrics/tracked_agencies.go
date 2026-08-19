package metrics

import (
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// lastTrackedAgencies remembers the last agency set emitted to the
// tracked-agency metrics so subsequent calls with an unchanged set are no-ops.
var (
	lastTrackedAgencies     map[trackedAgencyKey]struct{}
	trackedAgenciesReported bool
)

// reportTrackedAgencies updates the tracked-agency metrics to reflect the
// agencies Watchdog is currently observing. Agencies are the servers that
// passed config validation, so the count can be larger or smaller than the
// number of agencies present in any individual GTFS bundle.
//
// It reports the total count to the AgenciesTrackedCount Prometheus metric,
// and emits one AgenciesTrackedInfo series per agency labeled with its ID,
// name, and base URL.
//
// This is called once at startup and again only when the configured server set
// changes (e.g., a remote config refresh adds or removes an agency, or changes
// an agency's name or base URL), never on the periodic metric collection tick.
// Re-reporting an unchanged set is a no-op.
func reportTrackedAgencies(servers []models.ObaServer) {
	keys := trackedAgencyKeys(servers)
	if trackedAgenciesReported && sameAgencySet(keys, lastTrackedAgencies) {
		return
	}
	lastTrackedAgencies = keys
	trackedAgenciesReported = true

	AgenciesTrackedInfo.Reset()

	AgenciesTrackedCount.Set(float64(len(servers)))
	for _, server := range servers {
		AgenciesTrackedInfo.WithLabelValues(
			server.AgencyID,
			server.AgencyName,
			utils.SanitizeServerURL(server.ObaBaseURL),
		).Set(1)
	}
}
