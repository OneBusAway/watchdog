package history

import (
	"sort"
	"time"

	"watchdog.onebusaway.org/internal/geo"
)

// vehicleAnomalyType is a vehicle_anomaly_type.anomaly_code.
type vehicleAnomalyType string

const (
	anomalyStaleSourceTimestamp vehicleAnomalyType = "stale_source_timestamp"
	anomalyInvalidCoordinate    vehicleAnomalyType = "invalid_coordinate"
	anomalyStoppedOutOfBounds   vehicleAnomalyType = "stopped_out_of_bounds"
)

// vehicleAnomalyTypes lists every type the rules evaluate, in a fixed order
// so plans are deterministic.
var vehicleAnomalyTypes = []vehicleAnomalyType{
	anomalyStaleSourceTimestamp,
	anomalyInvalidCoordinate,
	anomalyStoppedOutOfBounds,
}

// vehicleCloseReason is a vehicle_anomaly_episode.close_reason.
type vehicleCloseReason string

const (
	closeFreshSourceTimestamp  vehicleCloseReason = "fresh_source_timestamp"
	closeNotPresentInFull      vehicleCloseReason = "not_present_in_full_dataset"
	closeCoordinatesValidAgain vehicleCloseReason = "coordinates_valid_again"
	closeInsideBoundingBox     vehicleCloseReason = "inside_bounding_box"
	closeNoLongerStoppedAtStop vehicleCloseReason = "no_longer_stopped_at_stop"
)

// The stale threshold is clamp(5 × fetch interval, 60 s, 300 s).
const (
	staleThresholdFetchMultiple = 5
	minStaleThreshold           = 60 * time.Second
	maxStaleThreshold           = 300 * time.Second
)

// staleThreshold is clamp(5 × fetch interval, 60 s, 300 s).
func staleThreshold(fetchInterval time.Duration) time.Duration {
	return min(max(staleThresholdFetchMultiple*fetchInterval, minStaleThreshold), maxStaleThreshold)
}

// vehicleFeed is one feed of a realtime snapshot. feedURL is the sanitized
// vehicle-position URL, the feed's identity in the database.
type vehicleFeed struct {
	feedURL         string
	observedAt      time.Time
	fullDataset     bool
	sourceTimestamp *time.Time
	payloadHash     string
	// reportedVehicleIDs holds every vehicle ID in the response, including
	// vehicles that agency-mode filtering left out of this entry's
	// observations. Nil means the observations are the whole response.
	reportedVehicleIDs map[string]struct{}
}

// reported says whether the response contained the vehicle at all.
func (f vehicleFeed) reported(vehicleID string, observed bool) bool {
	if f.reportedVehicleIDs == nil {
		return observed
	}
	_, ok := f.reportedVehicleIDs[vehicleID]
	return ok
}

// vehicleObservation is what the rules need to know about one vehicle in one
// response. Resolving attribution and the bounding box happens before
// planning, so the rules stay pure.
type vehicleObservation struct {
	feedURL   string
	vehicleID string
	// sourceTime is the vehicle's GTFS-Realtime timestamp, nil when absent.
	sourceTime     *time.Time
	positionReason geo.PositionValidationReason
	stoppedAtStop  bool
	// insideBox is nil when no bounding box applies.
	insideBox     *bool
	tripID        string
	tripStartDate string
	// facts holds what the vehicle's evidence row records. The rules do not
	// read it.
	facts vehicleFacts
}

// vehicleEpisodeKey identifies an episode: one anomaly type for one vehicle
// of one feed.
type vehicleEpisodeKey struct {
	feedURL   string
	vehicleID string
	anomaly   vehicleAnomalyType
}

// openVehicleEpisode is an open vehicle_anomaly_episode row held in memory,
// with the trip of its most recent evidence.
type openVehicleEpisode struct {
	episodeID     int64
	tripID        string
	tripStartDate string
}

// vehicleEpisodeOpen opens an episode with the vehicle's evidence.
type vehicleEpisodeOpen struct {
	key         vehicleEpisodeKey
	observation vehicleObservation
}

// vehicleEpisodeChange adds meaningful_change evidence to an open episode.
type vehicleEpisodeChange struct {
	key         vehicleEpisodeKey
	observation vehicleObservation
}

// vehicleEpisodeClose closes an open episode. observation is nil when the
// vehicle was absent from a full dataset, so no resolution evidence is saved.
type vehicleEpisodeClose struct {
	key         vehicleEpisodeKey
	reason      vehicleCloseReason
	observation *vehicleObservation
}

// vehicleEpisodePlan is what one tick changes for one server.
type vehicleEpisodePlan struct {
	opens   []vehicleEpisodeOpen
	changes []vehicleEpisodeChange
	closes  []vehicleEpisodeClose
}

// empty reports whether the tick changes nothing, so it needs no database call.
func (p vehicleEpisodePlan) empty() bool {
	return len(p.opens) == 0 && len(p.changes) == 0 && len(p.closes) == 0
}

// ruleOutcome is what one rule says about a present vehicle.
type ruleOutcome struct {
	anomalous bool
	// closeReason is set when the condition is clear. When neither is set
	// the rule cannot judge the vehicle and an open episode stays open.
	closeReason vehicleCloseReason
}

// evaluateRule applies one anomaly type's rule to a present vehicle.
func evaluateRule(anomaly vehicleAnomalyType, obs vehicleObservation, observedAt time.Time, threshold time.Duration) ruleOutcome {
	switch anomaly {
	case anomalyStaleSourceTimestamp:
		if obs.sourceTime == nil {
			return ruleOutcome{}
		}
		if observedAt.Sub(*obs.sourceTime) > threshold {
			return ruleOutcome{anomalous: true}
		}
		return ruleOutcome{closeReason: closeFreshSourceTimestamp}
	case anomalyInvalidCoordinate:
		if obs.positionReason != "" {
			return ruleOutcome{anomalous: true}
		}
		return ruleOutcome{closeReason: closeCoordinatesValidAgain}
	case anomalyStoppedOutOfBounds:
		// Invalid coordinates say nothing about where the vehicle is.
		if obs.positionReason != "" {
			return ruleOutcome{}
		}
		if !obs.stoppedAtStop {
			return ruleOutcome{closeReason: closeNoLongerStoppedAtStop}
		}
		if obs.insideBox == nil {
			return ruleOutcome{}
		}
		if !*obs.insideBox {
			return ruleOutcome{anomalous: true}
		}
		return ruleOutcome{closeReason: closeInsideBoundingBox}
	}
	return ruleOutcome{}
}

// planVehicleAnomalies compares one tick's vehicles with the open episodes in
// memory. Vehicles without an ID are skipped. A vehicle missing from a
// full-dataset response closes its open episodes; a differential feed's
// absence changes nothing. A vehicle that is in the response but not among
// the observations (another agency's vehicle on a shared feed) is left to
// the entry that observes it.
func planVehicleAnomalies(open map[vehicleEpisodeKey]*openVehicleEpisode, feeds []vehicleFeed, observations []vehicleObservation, threshold time.Duration) vehicleEpisodePlan {
	feedByURL := make(map[string]vehicleFeed, len(feeds))
	for _, feed := range feeds {
		feedByURL[feed.feedURL] = feed
	}

	var plan vehicleEpisodePlan
	type presentKey struct{ feedURL, vehicleID string }
	present := make(map[presentKey]bool, len(observations))
	for _, obs := range observations {
		feed, ok := feedByURL[obs.feedURL]
		pk := presentKey{obs.feedURL, obs.vehicleID}
		if !ok || obs.vehicleID == "" || present[pk] {
			continue
		}
		present[pk] = true
		for _, anomaly := range vehicleAnomalyTypes {
			key := vehicleEpisodeKey{feedURL: obs.feedURL, vehicleID: obs.vehicleID, anomaly: anomaly}
			planPresentVehicle(&plan, key, open[key], obs, evaluateRule(anomaly, obs, feed.observedAt, threshold))
		}
	}

	for key := range open {
		feed, ok := feedByURL[key.feedURL]
		if ok && feed.fullDataset && !feed.reported(key.vehicleID, present[presentKey{key.feedURL, key.vehicleID}]) {
			plan.closes = append(plan.closes, vehicleEpisodeClose{key: key, reason: closeNotPresentInFull})
		}
	}
	plan.sort()
	return plan
}

// planPresentVehicle adds what one rule's outcome means for one vehicle.
func planPresentVehicle(plan *vehicleEpisodePlan, key vehicleEpisodeKey, episode *openVehicleEpisode, obs vehicleObservation, outcome ruleOutcome) {
	switch {
	case episode == nil && outcome.anomalous:
		plan.opens = append(plan.opens, vehicleEpisodeOpen{key: key, observation: obs})
	case episode == nil:
	case outcome.closeReason != "":
		plan.closes = append(plan.closes, vehicleEpisodeClose{key: key, reason: outcome.closeReason, observation: &obs})
	case obs.tripID != episode.tripID || obs.tripStartDate != episode.tripStartDate:
		plan.changes = append(plan.changes, vehicleEpisodeChange{key: key, observation: obs})
	}
}

func (p *vehicleEpisodePlan) sort() {
	sort.Slice(p.opens, func(i, j int) bool { return p.opens[i].key.less(p.opens[j].key) })
	sort.Slice(p.changes, func(i, j int) bool { return p.changes[i].key.less(p.changes[j].key) })
	sort.Slice(p.closes, func(i, j int) bool { return p.closes[i].key.less(p.closes[j].key) })
}

func (k vehicleEpisodeKey) less(other vehicleEpisodeKey) bool {
	if k.feedURL != other.feedURL {
		return k.feedURL < other.feedURL
	}
	if k.vehicleID != other.vehicleID {
		return k.vehicleID < other.vehicleID
	}
	return k.anomaly < other.anomaly
}
