package config

import (
	"fmt"
	"strconv"
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
	mu       sync.RWMutex
	reported map[int]bool
	// reportedDuplicates tracks duplicate server IDs whose extra occurrences
	// were already reported, kept separate from reported so a duplicate report
	// can never be mistaken for a "recovered" invalid server.
	reportedDuplicates map[int]struct{}
}

// NewDroppedServersStore creates an empty DroppedServersStore.
func NewDroppedServersStore() *DroppedServersStore {
	return &DroppedServersStore{
		reported:           make(map[int]bool),
		reportedDuplicates: make(map[int]struct{}),
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
func (s *DroppedServersStore) Reconcile(servers []models.ObaServer) []models.ObaServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	valid := make([]models.ObaServer, 0, len(servers))
	present := make(map[int]struct{}, len(servers))

	for _, server := range servers {
		present[server.ID] = struct{}{}
		if err := ValidateServer(server); err != nil {
			if !s.reported[server.ID] {
				s.reported[server.ID] = true
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: map[string]string{
						"server_id":   strconv.Itoa(server.ID),
						"server_name": server.Name,
					},
					Level: sentry.LevelError,
				})
			}
			continue
		}

		if s.reported[server.ID] {
			delete(s.reported, server.ID)
			report.ReportErrorWithSentryOptions(
				newErrRecovered(server),
				report.SentryReportOptions{
					Tags: map[string]string{
						"server_id":   strconv.Itoa(server.ID),
						"server_name": server.Name,
					},
					Level: sentry.LevelInfo,
				},
			)
		}
		valid = append(valid, server)
	}

	for id := range s.reported {
		if _, ok := present[id]; !ok {
			delete(s.reported, id)
		}
	}

	return valid
}

// rejectDuplicateServerIDs drops all but the first server with a given ID,
// reporting each dropped duplicate to Sentry at most once per ID (tracked in a
// separate map from reported so a duplicate can never be misread as a
// previously invalid server that recovered).
//
// It must run before Reconcile: Reconcile keys its report-once state by server
// ID, so without this pre-pass a second invalid server sharing an ID with the
// first would be silently swallowed instead of reported. Duplicates whose ID
// disappears from the config are pruned, so a duplicate that reappears later
// reports again.
func (s *DroppedServersStore) rejectDuplicateServerIDs(servers []models.ObaServer) []models.ObaServer {
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[int]struct{}, len(servers))
	present := make(map[int]struct{}, len(servers))
	unique := make([]models.ObaServer, 0, len(servers))

	for _, server := range servers {
		present[server.ID] = struct{}{}
		if _, ok := seen[server.ID]; ok {
			if _, reported := s.reportedDuplicates[server.ID]; !reported {
				s.reportedDuplicates[server.ID] = struct{}{}
				report.ReportErrorWithSentryOptions(
					fmt.Errorf("duplicate server id %d (%q) dropped: keeping the first server with this id", server.ID, server.Name),
					report.SentryReportOptions{
						Tags: map[string]string{
							"server_id":   strconv.Itoa(server.ID),
							"server_name": server.Name,
						},
						Level: sentry.LevelError,
					},
				)
			}
			continue
		}
		seen[server.ID] = struct{}{}
		unique = append(unique, server)
	}

	for id := range s.reportedDuplicates {
		if _, ok := present[id]; !ok {
			delete(s.reportedDuplicates, id)
		}
	}

	return unique
}
