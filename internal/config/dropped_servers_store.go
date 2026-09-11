package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// DroppedServersStore remembers which servers have been reported to Sentry as
// invalid, so that a persistently broken entry in a dynamic remote config is
// reported once (on the valid->invalid transition) instead of on every refresh
// cycle.
type DroppedServersStore struct {
	mu                 sync.Mutex
	reported           map[string]struct{}
	reportedDuplicates map[string]struct{}
}

// NewDroppedServersStore creates an empty DroppedServersStore.
func NewDroppedServersStore() *DroppedServersStore {
	return &DroppedServersStore{
		reported:           make(map[string]struct{}),
		reportedDuplicates: make(map[string]struct{}),
	}
}

// Reconcile returns only the servers that pass ValidateServer, dropping and
// reporting each invalid server to Sentry exactly once so that one
// misconfigured entry (e.g. null feed URLs) cannot block monitoring of the rest
// of the fleet.
//
// Reporting is edge-triggered:
//   - invalid server not seen before -> error-level Sentry report
//   - invalid server already reported -> silent
//   - previously invalid server becomes valid -> info-level recovery report
//   - reported server disappears from the config -> pruned silently
//   - a new validation failure for an already-reported identity stays silent,
//     even when the specific failure reason changes
//
// reportedDuplicates tracks which identities are currently duplicated. A
// duplicate identity is reported to Sentry once when the second (or later) copy
// is dropped. The reportedDuplicates entry persists as long as the identity
// remains duplicated (multiple entries with the same key); when all but one
// copy are removed or become invalid, the entry is pruned. Local logging
// happens on every reconciliation cycle for each actively dropped duplicate,
// but Sentry reporting is transition-based (once per duplication episode).
func (s *DroppedServersStore) Reconcile(rawEntries []json.RawMessage, logger *slog.Logger) []models.ObaServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	valid := make([]models.ObaServer, 0, len(rawEntries))
	present := make(map[string]struct{}, len(rawEntries))
	seen := make(map[string]struct{}, len(rawEntries))
	duplicated := make(map[string]struct{})
	invalidThisCycle := make(map[string]struct{})
	previouslyReported := make(map[string]struct{}, len(s.reported))
	for identity := range s.reported {
		previouslyReported[identity] = struct{}{}
	}
	type recovery struct {
		server models.ObaServer
		tags   map[string]string
	}
	pendingRecoveries := make(map[string]recovery)

	for _, raw := range rawEntries {
		identity, tags, extra, _ := serverIdentityFromRaw(raw)
		present[identity] = struct{}{}

		// Decode first so that a malformed entry never claims the identity
		// in `seen` and blocks a valid later entry with the same key.
		server, err := decodeServerEntry(raw)
		if err != nil {
			invalidThisCycle[identity] = struct{}{}
			if _, alreadyReported := s.reported[identity]; !alreadyReported {
				s.reported[identity] = struct{}{}
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:         tags,
					ExtraContext: extra,
					Level:        sentry.LevelError,
				})
			}
			continue
		}

		if _, exists := seen[identity]; exists {
			duplicated[identity] = struct{}{}
			logger.Error("Dropping server with duplicate oba_base_url and agency_id",
				"agency_id", tags["agency_id"],
				"agency_name", tags["agency_name"],
				"oba_base_url", extra["oba_base_url"],
				"server_key", identity,
			)
			if _, alreadyReported := s.reportedDuplicates[identity]; !alreadyReported {
				s.reportedDuplicates[identity] = struct{}{}
				report.ReportErrorWithSentryOptions(fmt.Errorf("duplicate server key %q", identity), report.SentryReportOptions{
					Tags: tags,
					ExtraContext: map[string]interface{}{
						"oba_base_url": extra["oba_base_url"],
						"server_key":   identity,
					},
					Level: sentry.LevelError,
				})
			}
			continue
		}
		seen[identity] = struct{}{}

		if _, wasReported := previouslyReported[identity]; wasReported {
			pendingRecoveries[identity] = recovery{server: server, tags: tags}
		}
		valid = append(valid, server)
	}

	for identity, recovered := range pendingRecoveries {
		if _, stillInvalid := invalidThisCycle[identity]; stillInvalid {
			continue
		}
		delete(s.reported, identity)
		report.ReportErrorWithSentryOptions(
			newErrRecovered(recovered.server),
			report.SentryReportOptions{
				Tags:         recovered.tags,
				ExtraContext: map[string]interface{}{"oba_base_url": recovered.server.ObaBaseURL},
				Level:        sentry.LevelInfo,
			},
		)
	}

	for identity := range s.reported {
		if _, ok := present[identity]; !ok {
			delete(s.reported, identity)
		}
	}
	for identity := range s.reportedDuplicates {
		if _, stillDuplicated := duplicated[identity]; !stillDuplicated {
			delete(s.reportedDuplicates, identity)
		}
	}

	return valid
}

// serverIdentityFromRaw extracts a stable identity before validation. Entries
// missing oba_base_url cannot share the normal composite key, so their raw JSON
// is used to keep unrelated malformed entries from suppressing each other.
func serverIdentityFromRaw(raw json.RawMessage) (identity string, tags map[string]string, extra map[string]interface{}, duplicateEligible bool) {
	var fields struct {
		ServerName string `json:"server_name"`
		LegacyName string `json:"name"`
		AgencyName string `json:"agency_name"`
		AgencyID   string `json:"agency_id"`
		ObaBaseURL string `json:"oba_base_url"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "raw:" + string(raw), nil, nil, false
	}
	if fields.AgencyName == "" {
		fields.AgencyName = fields.LegacyName
	}
	tags = make(map[string]string, 3)
	if fields.ServerName != "" {
		tags["server_name"] = fields.ServerName
	}
	if fields.AgencyName != "" {
		tags["agency_name"] = fields.AgencyName
	}
	if fields.AgencyID != "" {
		tags["agency_id"] = fields.AgencyID
	}
	extra = map[string]interface{}{"oba_base_url": fields.ObaBaseURL}
	if fields.ObaBaseURL == "" {
		return "raw:" + string(raw), tags, extra, false
	}
	return models.ServerKey(fields.ObaBaseURL, fields.AgencyID), tags, extra, true
}
