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
	server models.ObaServer
	feeds  map[string]staticFeedMappingSeries
}

// StaticFeedMappingStore tracks the exact series emitted for each configured
// entry and source feed so a successful reparse can retire relationships that
// changed. Failed downloads leave the last successful result intact.
type StaticFeedMappingStore struct {
	mu      sync.Mutex
	entries map[string]staticFeedMappingEntry
}

func NewStaticFeedMappingStore() *StaticFeedMappingStore {
	return &StaticFeedMappingStore{entries: make(map[string]staticFeedMappingEntry)}
}

func (s *StaticFeedMappingStore) Observe(server models.ObaServer, observation gtfs.StaticFeedMappingObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := server.ServerKey()
	entry, exists := s.entries[key]
	if !exists {
		entry = staticFeedMappingEntry{server: server, feeds: make(map[string]staticFeedMappingSeries)}
	}
	previousServer := entry.server
	current := entry.feeds
	if previousServer.ServerName != server.ServerName || utils.SanitizeServerURL(previousServer.ObaBaseURL) != utils.SanitizeServerURL(server.ObaBaseURL) {
		for feedURL, series := range current {
			deleteStaticFeedMappingSeries(previousServer, feedURL, series)
			emitStaticFeedMappingSeries(server, feedURL, series)
		}
		previousServer = server
	}
	configured := make(map[string]bool, len(observation.ConfiguredFeedURLs))
	for _, feedURL := range observation.ConfiguredFeedURLs {
		configured[feedURL] = true
	}
	for feedURL, series := range current {
		if configured[feedURL] {
			continue
		}
		deleteStaticFeedMappingSeries(previousServer, feedURL, series)
		delete(current, feedURL)
	}

	for _, result := range observation.Results {
		if previous, ok := current[result.FeedURL]; ok {
			deleteStaticFeedMappingSeries(previousServer, result.FeedURL, previous)
		}
		next := staticFeedMappingSeries{
			mappings: append([]gtfs.StaticFeedAgencyMapping(nil), result.Mappings...),
			failures: append([]gtfs.StaticFeedMappingFailure(nil), result.Failures...),
		}
		current[result.FeedURL] = next
		emitStaticFeedMappingSeries(server, result.FeedURL, next)
	}
	entry.server = server
	s.entries[key] = entry
}

func (s *StaticFeedMappingStore) Prune(keep func(string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := make([]string, 0)
	for key, entry := range s.entries {
		if keep(key) {
			continue
		}
		for feedURL, series := range entry.feeds {
			deleteStaticFeedMappingSeries(entry.server, feedURL, series)
		}
		delete(s.entries, key)
		removed = append(removed, key)
	}
	return removed
}

func emitStaticFeedMappingSeries(server models.ObaServer, feedURL string, series staticFeedMappingSeries) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	for _, mapping := range series.mappings {
		GtfsStaticFeedAgencyMappingInfo.WithLabelValues(feedURL, mapping.AgencyID, mapping.AgencyName, server.ServerName, serverURL).Set(1)
	}
	for _, failure := range series.failures {
		GtfsStaticFeedAgencyMappingFailure.WithLabelValues(feedURL, server.ServerName, serverURL, string(failure.Reason)).Set(1)
	}
}

func deleteStaticFeedMappingSeries(server models.ObaServer, feedURL string, series staticFeedMappingSeries) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	for _, mapping := range series.mappings {
		GtfsStaticFeedAgencyMappingInfo.DeleteLabelValues(feedURL, mapping.AgencyID, mapping.AgencyName, server.ServerName, serverURL)
	}
	for _, failure := range series.failures {
		GtfsStaticFeedAgencyMappingFailure.DeleteLabelValues(feedURL, server.ServerName, serverURL, string(failure.Reason))
	}
}
