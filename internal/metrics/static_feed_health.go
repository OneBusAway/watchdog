package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type staticFeedHealthRecord struct {
	server      models.ObaServer
	feedURL     string
	observation gtfs.StaticFeedRefreshObservation
	lastSuccess int64
}

type StaticFeedHealthStore struct {
	mu      sync.Mutex
	records map[string]staticFeedHealthRecord
}

func NewStaticFeedHealthStore() *StaticFeedHealthStore {
	return &StaticFeedHealthStore{records: make(map[string]staticFeedHealthRecord)}
}

func staticFeedHealthKey(server models.ObaServer, feedURL string) string {
	return server.ServerKey() + "|" + feedURL
}

func staticFeedLabels(server models.ObaServer, feedURL string) prometheus.Labels {
	return prometheus.Labels{
		"feed_url":    utils.SanitizeServerURL(feedURL),
		"server_name": server.ServerName,
		"server_url":  utils.SanitizeServerURL(server.ObaBaseURL),
	}
}

func deleteStaticFeedMetrics(server models.ObaServer, feedURL string) {
	labels := staticFeedLabels(server, feedURL)
	GtfsStaticFeedFetchSuccess.DeletePartialMatch(labels)
	GtfsStaticFeedLastAttempt.DeletePartialMatch(labels)
	GtfsStaticFeedLastSuccessfulFetch.DeletePartialMatch(labels)
	GtfsStaticFeedFailureSince.DeletePartialMatch(labels)
	GtfsStaticFeedConservativeMode.DeletePartialMatch(labels)
	GtfsStaticFeedFailureInfo.DeletePartialMatch(labels)
}

func reportStaticFeedHealth(record staticFeedHealthRecord) {
	labels := []string{utils.SanitizeServerURL(record.feedURL), record.server.ServerName, utils.SanitizeServerURL(record.server.ObaBaseURL)}
	observation := record.observation
	GtfsStaticFeedFetchSuccess.WithLabelValues(labels...).Set(boolValue(observation.Success))
	GtfsStaticFeedLastAttempt.WithLabelValues(labels...).Set(float64(observation.AttemptedAt.UTC().Unix()))
	GtfsStaticFeedConservativeMode.WithLabelValues(labels...).Set(boolValue(observation.Conservative))
	if record.lastSuccess > 0 {
		GtfsStaticFeedLastSuccessfulFetch.WithLabelValues(labels...).Set(float64(record.lastSuccess))
	}
	if observation.Success {
		GtfsStaticFeedFailureSince.WithLabelValues(labels...).Set(0)
		return
	}
	if !observation.FailureSince.IsZero() {
		GtfsStaticFeedFailureSince.WithLabelValues(labels...).Set(float64(observation.FailureSince.UTC().Unix()))
	}
	GtfsStaticFeedFailureInfo.WithLabelValues(append(labels, string(observation.Stage), string(observation.Reason))...).Set(1)
}

func (s *StaticFeedHealthStore) Observe(server models.ObaServer, feedURL string, observation gtfs.StaticFeedRefreshObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := staticFeedHealthKey(server, feedURL)
	if previous, ok := s.records[key]; ok {
		deleteStaticFeedMetrics(previous.server, previous.feedURL)
	}
	record := staticFeedHealthRecord{server: server, feedURL: feedURL, observation: observation}
	if previous, ok := s.records[key]; ok {
		record.lastSuccess = previous.lastSuccess
	}
	if observation.Success {
		record.lastSuccess = observation.AttemptedAt.UTC().Unix()
	}
	s.records[key] = record
	reportStaticFeedHealth(record)
}

func (s *StaticFeedHealthStore) Reconcile(servers []models.ObaServer) {
	configured := make(map[string]models.ObaServer)
	for _, server := range servers {
		for _, feedURL := range server.GtfsStaticFeeds {
			configured[staticFeedHealthKey(server, feedURL)] = server
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, record := range s.records {
		server, ok := configured[key]
		if ok && record.server.ServerName == server.ServerName && record.server.ObaBaseURL == server.ObaBaseURL {
			continue
		}
		deleteStaticFeedMetrics(record.server, record.feedURL)
		if !ok {
			delete(s.records, key)
			continue
		}
		record.server = server
		s.records[key] = record
		reportStaticFeedHealth(record)
	}
}
