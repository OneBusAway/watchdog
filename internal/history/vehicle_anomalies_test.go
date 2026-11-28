package history

import (
	"reflect"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
)

var planObservedAt = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

const (
	planThreshold = 2 * time.Minute
	planFeedURL   = "https://feed.example/vehicle-positions"
	planSourceKey = "source-a"
)

func boolPtr(b bool) *bool { return &b }

func timePtr(t time.Time) *time.Time { return &t }

// healthyVehicle is present, fresh, valid and stopped inside its box, so no
// rule finds an anomaly.
func healthyVehicle(id string) vehicleObservation {
	return vehicleObservation{
		feedURL:       planFeedURL,
		vehicleID:     id,
		sourceTime:    timePtr(planObservedAt.Add(-10 * time.Second)),
		stoppedAtStop: true,
		insideBox:     boolPtr(true),
		tripID:        "trip-1",
		tripStartDate: "20261127",
	}
}

func fullFeed() []vehicleFeed {
	return []vehicleFeed{{feedURL: planFeedURL, sourceKey: planSourceKey, observedAt: planObservedAt, fullDataset: true}}
}

func differentialFeed() []vehicleFeed {
	return []vehicleFeed{{feedURL: planFeedURL, sourceKey: planSourceKey, observedAt: planObservedAt, fullDataset: false}}
}

func keyFor(id string, anomaly vehicleAnomalyType) vehicleEpisodeKey {
	return vehicleEpisodeKey{feedURL: planFeedURL, vehicleID: id, anomaly: anomaly}
}

func openFor(keys ...vehicleEpisodeKey) map[vehicleEpisodeKey]*openVehicleEpisode {
	open := make(map[vehicleEpisodeKey]*openVehicleEpisode, len(keys))
	for i, key := range keys {
		open[key] = &openVehicleEpisode{episodeID: int64(i + 1), tripID: "trip-1", tripStartDate: "20261127"}
	}
	return open
}

// sightingsFor gives every vehicle with an open episode a sighting from
// planSourceKey, a minute before planObservedAt.
func sightingsFor(open map[vehicleEpisodeKey]*openVehicleEpisode) map[vehicleSightingKey]vehicleSighting {
	sightings := make(map[vehicleSightingKey]vehicleSighting, len(open))
	for key := range open {
		sightings[vehicleSightingKey{feedURL: key.feedURL, vehicleID: key.vehicleID}] = vehicleSighting{sourceKey: planSourceKey, observedAt: planObservedAt.Add(-time.Minute)}
	}
	return sightings
}

func openedKeys(plan vehicleEpisodePlan) []vehicleEpisodeKey {
	keys := make([]vehicleEpisodeKey, 0, len(plan.opens))
	for _, o := range plan.opens {
		keys = append(keys, o.key)
	}
	return keys
}

func TestStaleThreshold(t *testing.T) {
	tests := []struct {
		fetchInterval time.Duration
		want          time.Duration
	}{
		{5 * time.Second, 60 * time.Second},
		{30 * time.Second, 150 * time.Second},
		{2 * time.Minute, 300 * time.Second},
	}
	for _, tt := range tests {
		if got := staleThreshold(tt.fetchInterval); got != tt.want {
			t.Errorf("staleThreshold(%s) = %s, want %s", tt.fetchInterval, got, tt.want)
		}
	}
}

func TestVehicle_HealthyVehicleOpensNothing(t *testing.T) {
	plan := planVehicleAnomalies(nil, nil, fullFeed(), []vehicleObservation{healthyVehicle("bus-1")}, planThreshold)
	if !plan.empty() {
		t.Fatalf("plan = %+v, want empty", plan)
	}
}

func TestVehicle_OpensEachType(t *testing.T) {
	stale := healthyVehicle("bus-1")
	stale.sourceTime = timePtr(planObservedAt.Add(-planThreshold - time.Second))

	invalid := healthyVehicle("bus-1")
	invalid.positionReason = geo.PositionPlaceholderZeroCoordinate

	outside := healthyVehicle("bus-1")
	outside.insideBox = boolPtr(false)

	tests := []struct {
		name string
		obs  vehicleObservation
		want vehicleAnomalyType
	}{
		{"stale source timestamp", stale, anomalyStaleSourceTimestamp},
		{"invalid coordinate", invalid, anomalyInvalidCoordinate},
		{"stopped out of bounds", outside, anomalyStoppedOutOfBounds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planVehicleAnomalies(nil, nil, fullFeed(), []vehicleObservation{tt.obs}, planThreshold)
			want := []vehicleEpisodeKey{keyFor("bus-1", tt.want)}
			if got := openedKeys(plan); !reflect.DeepEqual(got, want) {
				t.Fatalf("opened %v, want %v", got, want)
			}
			if len(plan.closes) != 0 || len(plan.changes) != 0 {
				t.Fatalf("unexpected closes or changes: %+v", plan)
			}
		})
	}
}

func TestVehicle_StaleAtThresholdIsFresh(t *testing.T) {
	obs := healthyVehicle("bus-1")
	obs.sourceTime = timePtr(planObservedAt.Add(-planThreshold))
	if plan := planVehicleAnomalies(nil, nil, fullFeed(), []vehicleObservation{obs}, planThreshold); !plan.empty() {
		t.Fatalf("an age equal to the threshold opened %+v", plan)
	}
}

func TestVehicle_ClosesEachReason(t *testing.T) {
	moving := healthyVehicle("bus-1")
	moving.stoppedAtStop = false

	tests := []struct {
		name    string
		anomaly vehicleAnomalyType
		obs     vehicleObservation
		want    vehicleCloseReason
	}{
		{"fresh source timestamp", anomalyStaleSourceTimestamp, healthyVehicle("bus-1"), closeFreshSourceTimestamp},
		{"coordinates valid again", anomalyInvalidCoordinate, healthyVehicle("bus-1"), closeCoordinatesValidAgain},
		{"inside bounding box", anomalyStoppedOutOfBounds, healthyVehicle("bus-1"), closeInsideBoundingBox},
		{"no longer stopped at stop", anomalyStoppedOutOfBounds, moving, closeNoLongerStoppedAtStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := keyFor("bus-1", tt.anomaly)
			open := openFor(key)
			plan := planVehicleAnomalies(open, sightingsFor(open), fullFeed(), []vehicleObservation{tt.obs}, planThreshold)
			if len(plan.closes) != 1 || plan.closes[0].key != key || plan.closes[0].reason != tt.want {
				t.Fatalf("closes = %+v, want %s for %v", plan.closes, tt.want, key)
			}
			if plan.closes[0].observation == nil {
				t.Fatal("a present vehicle's close must carry resolution evidence")
			}
		})
	}
}

func TestVehicle_NotPresentInFullDatasetCloses(t *testing.T) {
	keys := []vehicleEpisodeKey{keyFor("gone", anomalyInvalidCoordinate), keyFor("gone", anomalyStaleSourceTimestamp)}
	open := openFor(keys...)
	plan := planVehicleAnomalies(open, sightingsFor(open), fullFeed(), []vehicleObservation{healthyVehicle("other")}, planThreshold)

	if len(plan.closes) != 2 {
		t.Fatalf("closes = %+v, want both episodes of the absent vehicle", plan.closes)
	}
	for _, c := range plan.closes {
		if c.reason != closeNotPresentInFull || c.observation != nil {
			t.Errorf("close %+v, want not_present_in_full_dataset with no evidence", c)
		}
	}
}

func TestVehicle_DifferentialAbsenceIgnored(t *testing.T) {
	open := openFor(keyFor("gone", anomalyInvalidCoordinate))
	if plan := planVehicleAnomalies(open, sightingsFor(open), differentialFeed(), nil, planThreshold); !plan.empty() {
		t.Fatalf("absence from a differential feed changed %+v", plan)
	}
}

func TestVehicle_MeaningfulChangeOnlyOnTripChange(t *testing.T) {
	key := keyFor("bus-1", anomalyInvalidCoordinate)
	invalid := healthyVehicle("bus-1")
	invalid.positionReason = geo.PositionMissing

	open := openFor(key)
	if plan := planVehicleAnomalies(open, sightingsFor(open), fullFeed(), []vehicleObservation{invalid}, planThreshold); !plan.empty() {
		t.Fatalf("same trip added %+v", plan)
	}

	for _, change := range []func(*vehicleObservation){
		func(o *vehicleObservation) { o.tripID = "trip-2" },
		func(o *vehicleObservation) { o.tripStartDate = "20261128" },
	} {
		obs := invalid
		change(&obs)
		open := openFor(key)
		plan := planVehicleAnomalies(open, sightingsFor(open), fullFeed(), []vehicleObservation{obs}, planThreshold)
		if len(plan.changes) != 1 || plan.changes[0].key != key || len(plan.opens) != 0 || len(plan.closes) != 0 {
			t.Fatalf("plan = %+v, want one meaningful change for %v", plan, key)
		}
	}
}

// When a rule cannot judge the vehicle, its open episode stays open.
func TestVehicle_UnjudgedVehicleKeepsEpisodeOpen(t *testing.T) {
	noTimestamp := healthyVehicle("bus-1")
	noTimestamp.sourceTime = nil

	invalidCoords := healthyVehicle("bus-1")
	invalidCoords.positionReason = geo.PositionNonFiniteCoordinate

	noBox := healthyVehicle("bus-1")
	noBox.insideBox = nil

	tests := []struct {
		name    string
		anomaly vehicleAnomalyType
		obs     vehicleObservation
	}{
		{"stale without a timestamp", anomalyStaleSourceTimestamp, noTimestamp},
		{"out of bounds with invalid coordinates", anomalyStoppedOutOfBounds, invalidCoords},
		{"out of bounds without a bounding box", anomalyStoppedOutOfBounds, noBox},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open := openFor(keyFor("bus-1", tt.anomaly))
			plan := planVehicleAnomalies(open, sightingsFor(open), fullFeed(), []vehicleObservation{tt.obs}, planThreshold)
			for _, c := range plan.closes {
				if c.key.anomaly == tt.anomaly {
					t.Fatalf("closed %+v", c)
				}
			}
		})
	}
}

func TestVehicle_NoIDSkipped(t *testing.T) {
	obs := healthyVehicle("")
	obs.positionReason = geo.PositionMissing
	if plan := planVehicleAnomalies(nil, nil, fullFeed(), []vehicleObservation{obs}, planThreshold); !plan.empty() {
		t.Fatalf("a vehicle without an ID produced %+v", plan)
	}
}

func TestVehicle_DuplicateIDUsesFirstObservation(t *testing.T) {
	invalid := healthyVehicle("bus-1")
	invalid.positionReason = geo.PositionMissing
	plan := planVehicleAnomalies(nil, nil, fullFeed(), []vehicleObservation{invalid, healthyVehicle("bus-1")}, planThreshold)
	want := []vehicleEpisodeKey{keyFor("bus-1", anomalyInvalidCoordinate)}
	if got := openedKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("opened %v, want %v", got, want)
	}
}

func TestVehicle_PlanIsSorted(t *testing.T) {
	var observations []vehicleObservation
	for _, id := range []string{"c", "a", "b"} {
		obs := healthyVehicle(id)
		obs.positionReason = geo.PositionMissing
		obs.sourceTime = timePtr(planObservedAt.Add(-time.Hour))
		observations = append(observations, obs)
	}
	plan := planVehicleAnomalies(nil, nil, fullFeed(), observations, planThreshold)
	var want []vehicleEpisodeKey
	for _, id := range []string{"a", "b", "c"} {
		want = append(want, keyFor(id, anomalyInvalidCoordinate), keyFor(id, anomalyStaleSourceTimestamp))
	}
	if got := openedKeys(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("opened %v, want %v", got, want)
	}
}

// Feeds that differ only by query string share a feed URL. A full dataset
// from one of them says nothing about the other's vehicles.
func TestVehicle_AbsenceFromAnotherSourceIgnored(t *testing.T) {
	open := openFor(keyFor("other-agency-bus", anomalyInvalidCoordinate))
	feeds := fullFeed()
	feeds[0].sourceKey = "source-b"
	if plan := planVehicleAnomalies(open, sightingsFor(open), feeds, nil, planThreshold); !plan.empty() {
		t.Fatalf("another source's full dataset changed %+v", plan)
	}
}

// After a restart a vehicle's source can be unknown; absence then cannot
// tell whether the vehicle left, so it changes nothing.
func TestVehicle_AbsenceWithUnknownSourceIgnored(t *testing.T) {
	open := openFor(keyFor("bus-1", anomalyInvalidCoordinate))
	sightings := sightingsFor(open)
	for key, sighting := range sightings {
		sighting.sourceKey = ""
		sightings[key] = sighting
	}
	if plan := planVehicleAnomalies(open, sightings, fullFeed(), nil, planThreshold); !plan.empty() {
		t.Fatalf("absence closed an episode of unknown source: %+v", plan)
	}
}

// A response older than the vehicle's newest sighting, recorded late because
// entries fetch concurrently, neither opens, closes nor changes anything.
// This holds after the episode closed too, so a late anomalous response
// cannot open a new episode for an old condition.
func TestVehicle_StaleResponseIgnored(t *testing.T) {
	key := keyFor("bus-1", anomalyInvalidCoordinate)
	open := openFor(key)
	newer := map[vehicleSightingKey]vehicleSighting{
		{feedURL: planFeedURL, vehicleID: "bus-1"}: {sourceKey: planSourceKey, observedAt: planObservedAt.Add(time.Minute)},
	}

	if plan := planVehicleAnomalies(open, newer, fullFeed(), []vehicleObservation{healthyVehicle("bus-1")}, planThreshold); !plan.empty() {
		t.Fatalf("an older healthy response changed %+v", plan)
	}
	if plan := planVehicleAnomalies(open, newer, fullFeed(), nil, planThreshold); !plan.empty() {
		t.Fatalf("an older response without the vehicle changed %+v", plan)
	}

	invalid := healthyVehicle("bus-1")
	invalid.positionReason = geo.PositionMissing
	if plan := planVehicleAnomalies(nil, newer, fullFeed(), []vehicleObservation{invalid}, planThreshold); !plan.empty() {
		t.Fatalf("an older anomalous response opened %+v after a newer one", plan)
	}
}
