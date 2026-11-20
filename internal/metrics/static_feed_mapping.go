package metrics

import (
	"sync"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type staticFeedMappingSeries struct {
	mappings []gtfs.StaticFeedAgencyMapping
	failures []gtfs.StaticFeedMappingFailure
}

type staticFeedMappingEntry struct {
	server  models.ObaServer
	feedURL string
	series  staticFeedMappingSeries
}

// StaticFeedMappingStore tracks the exact series emitted for each configured
// entry's static feed so a successful reparse can retire relationships that
// changed. Failed downloads leave the last successful result intact.
type StaticFeedMappingStore struct {
	mu      sync.Mutex
	entries map[string]staticFeedMappingEntry
}

func NewStaticFeedMappingStore() *StaticFeedMappingStore {
	return &StaticFeedMappingStore{entries: make(map[string]staticFeedMappingEntry)}
}

// Observe replaces the entry's series with the latest parse. The previous
// series are deleted under the identity they were emitted with, so a renamed
// server or a changed feed URL never leaves a stale series behind.
func (s *StaticFeedMappingStore) Observe(server models.ObaServer, observation gtfs.StaticFeedMappingObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := server.ServerKey()
	if previous, exists := s.entries[key]; exists {
		deleteStaticFeedMappingSeries(previous.server, previous.feedURL, previous.series)
	}
	next := staticFeedMappingEntry{
		server:  server,
		feedURL: observation.FeedURL,
		series: staticFeedMappingSeries{
			mappings: append([]gtfs.StaticFeedAgencyMapping(nil), observation.Mappings...),
			failures: append([]gtfs.StaticFeedMappingFailure(nil), observation.Failures...),
		},
	}
	emitStaticFeedMappingSeries(next.server, next.feedURL, next.series)
	s.entries[key] = next
}

func (s *StaticFeedMappingStore) Prune(keep func(string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := make([]string, 0)
	for key, entry := range s.entries {
		if keep(key) {
			continue
		}
		deleteStaticFeedMappingSeries(entry.server, entry.feedURL, entry.series)
		delete(s.entries, key)
		removed = append(removed, key)
	}
	return removed
}

func emitStaticFeedMappingSeries(server models.ObaServer, feedURL string, series staticFeedMappingSeries) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	feedURL = utils.SanitizeServerURL(feedURL)
	for _, mapping := range series.mappings {
		GtfsStaticFeedAgencyMappingInfo.WithLabelValues(feedURL, mapping.AgencyID, mapping.AgencyName, server.ServerName, serverURL).Set(1)
	}
	for _, failure := range series.failures {
		GtfsStaticFeedAgencyMappingFailure.WithLabelValues(feedURL, server.ServerName, serverURL, string(failure.Reason)).Set(1)
	}
}

func deleteStaticFeedMappingSeries(server models.ObaServer, feedURL string, series staticFeedMappingSeries) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	feedURL = utils.SanitizeServerURL(feedURL)
	for _, mapping := range series.mappings {
		GtfsStaticFeedAgencyMappingInfo.DeleteLabelValues(feedURL, mapping.AgencyID, mapping.AgencyName, server.ServerName, serverURL)
	}
	for _, failure := range series.failures {
		GtfsStaticFeedAgencyMappingFailure.DeleteLabelValues(feedURL, server.ServerName, serverURL, string(failure.Reason))
	}
}
