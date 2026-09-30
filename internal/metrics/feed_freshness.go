package metrics

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type feedFreshnessState struct {
	payloadHash string
	changedAt   int64
	labels      prometheus.Labels
}

type FeedFreshnessStore struct {
	mu    sync.Mutex
	state map[string]map[string]feedFreshnessState
}

func NewFeedFreshnessStore() *FeedFreshnessStore {
	return &FeedFreshnessStore{state: make(map[string]map[string]feedFreshnessState)}
}

func (s *FeedFreshnessStore) Observe(server models.ObaServer, observation gtfs.FeedObservation) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	labels := prometheus.Labels{
		"agency_id": server.AgencyID, "agency_name": server.AgencyName,
		"server_name": server.ServerName, "server_url": serverURL,
		"feed": observation.FeedID, "feed_url": observation.FeedURL,
	}
	GtfsRtFeedFetchSuccess.With(labels).Set(boolFloat(observation.Success))
	serverKey := server.ServerKey()
	feedKey := observation.FeedID + "|" + observation.FeedURL
	s.mu.Lock()
	if s.state[serverKey] == nil {
		s.state[serverKey] = make(map[string]feedFreshnessState)
	}
	state, exists := s.state[serverKey][feedKey]
	state.labels = labels
	s.state[serverKey][feedKey] = state
	s.mu.Unlock()
	if !observation.Success {
		// Current-value metrics disappear rather than falsely reporting zero or
		// retaining the last successful snapshot.
		GtfsRtFeedSourceTimestamp.Delete(labels)
		GtfsRtFeedVehicleEntities.Delete(labels)
		GtfsRtFeedVehicleTimestampMissingCount.Delete(labels)
		return
	}

	observedUnix := float64(observation.ObservedAt.Unix())
	GtfsRtFeedLastSuccessfulFetchTimestamp.With(labels).Set(observedUnix)
	GtfsRtFeedVehicleEntities.With(labels).Set(float64(observation.VehicleEntities))
	GtfsRtFeedVehicleTimestampMissingCount.With(labels).Set(float64(observation.VehicleTimestampMissing))
	if observation.SourceTimestamp != nil {
		GtfsRtFeedSourceTimestamp.With(labels).Set(float64(observation.SourceTimestamp.Unix()))
	} else {
		GtfsRtFeedSourceTimestamp.Delete(labels)
	}

	s.mu.Lock()
	state = s.state[serverKey][feedKey]
	if !exists || state.payloadHash != observation.PayloadHash {
		state.payloadHash = observation.PayloadHash
		state.changedAt = observation.ObservedAt.Unix()
	}
	s.state[serverKey][feedKey] = state
	s.mu.Unlock()
	GtfsRtFeedPayloadLastChangedTimestamp.With(labels).Set(float64(state.changedAt))
}

func (s *FeedFreshnessStore) Reconcile(server models.ObaServer) []string {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	desired := make(map[string]prometheus.Labels, len(server.GtfsRTFeeds))
	for index, feed := range server.GtfsRTFeeds {
		feedID := fmt.Sprintf("%d", index)
		key := feedID + "|" + utils.SanitizeServerURL(feed.VehiclePositionURL)
		desired[key] = prometheus.Labels{
			"agency_id": server.AgencyID, "agency_name": server.AgencyName,
			"server_name": server.ServerName, "server_url": serverURL,
			"feed": feedID, "feed_url": utils.SanitizeServerURL(feed.VehiclePositionURL),
		}
	}

	s.mu.Lock()
	var removed []feedFreshnessState
	removedIDs := make(map[string]bool)
	for key, state := range s.state[server.ServerKey()] {
		labels, exists := desired[key]
		if !exists || !reflect.DeepEqual(labels, state.labels) {
			removed = append(removed, state)
			removedIDs[state.labels["feed"]] = true
			delete(s.state[server.ServerKey()], key)
		}
	}
	s.mu.Unlock()
	for _, state := range removed {
		deleteFeedFreshnessSeries(state.labels)
	}
	feedIDs := make([]string, 0, len(removedIDs))
	for feedID := range removedIDs {
		feedIDs = append(feedIDs, feedID)
	}
	return feedIDs
}

func deleteFeedFreshnessSeries(labels prometheus.Labels) {
	GtfsRtFeedFetchSuccess.Delete(labels)
	GtfsRtFeedLastSuccessfulFetchTimestamp.Delete(labels)
	GtfsRtFeedSourceTimestamp.Delete(labels)
	GtfsRtFeedPayloadLastChangedTimestamp.Delete(labels)
	GtfsRtFeedVehicleEntities.Delete(labels)
	GtfsRtFeedVehicleTimestampMissingCount.Delete(labels)
}

func (s *FeedFreshnessStore) Prune(keep func(serverKey string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for serverKey := range s.state {
		if !keep(serverKey) {
			removed = append(removed, serverKey)
			delete(s.state, serverKey)
		}
	}
	return removed
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
