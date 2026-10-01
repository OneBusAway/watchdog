package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// Scope dispatch for the three GTFS-RT metric passes below.
//
// Each pass takes an `agencies` slice alongside the server entry:
//
//   - nil (agency-mode): the realtime store already contains only vehicles
//     resolved to the configured agency. The pass trusts that filtered
//     snapshot and does not re-filter it.
//   - non-nil (server-mode): the entry is server-scoped and the feed is the
//     merged feed for every agency the server reports. Each vehicle is
//     attributed to an agency through its TripDescriptor.route_id/trip_id, and the
//     pass runs ONCE per server per tick — not once per agency. Running it per
//     agency would multiply transition observations and file every vehicle
//     under every agency's last-seen slot.
//
// In server-mode the realtime feed is read from the server-scoped key
// (models.ServerKey(oba_base_url, "")), while an attributed vehicle uses its
// agency-scoped bounding box. The server-scoped box remains the fallback for
// unattributed vehicles.

// agencyIndex keys the OBA-reported agency entries by agency_id so attribution is an
// O(1) lookup. It returns nil for agency-mode, whose store is pre-filtered.
func agencyIndex(agencies []models.ObaServer) map[string]models.ObaServer {
	if len(agencies) == 0 {
		return nil
	}
	byID := make(map[string]models.ObaServer, len(agencies))
	for _, agency := range agencies {
		byID[agency.AgencyID] = agency
	}
	return byID
}

// attributeVehicle resolves the agency a realtime vehicle belongs to.
//
// In agency-mode the store has already applied the shared resolver, so the
// configured entry is returned for each retained vehicle. In server-mode the
// resolver checks both route_id and trip_id; conflicts and unknown vehicles are
// unattributed and remain in the existing server-scoped quality paths.
type vehicleAttribution struct {
	agency     models.ObaServer
	candidates []string
	reason     gtfs.AttributionFailureReason
}

func (a vehicleAttribution) resolved() bool { return a.reason == "" }

func attributeVehicle(server models.ObaServer, agencyByID map[string]models.ObaServer, routeAgencyIndex gtfs.VehicleAttributionResolver, vehicle remoteGtfs.Vehicle) vehicleAttribution {
	if agencyByID == nil {
		return vehicleAttribution{agency: server}
	}
	var routeID, tripID string
	if vehicle.Trip != nil {
		routeID = vehicle.Trip.ID.RouteID
		tripID = vehicle.Trip.ID.ID
	}
	resolution := gtfs.AttributionResult{Reason: gtfs.AttributionMissingRouteID}
	if routeAgencyIndex != nil {
		resolution = routeAgencyIndex.ResolveVehicleAttribution(server.ServerKey(), routeID, tripID)
	}
	if vehicle.ID == nil || vehicle.ID.ID == "" {
		return vehicleAttribution{candidates: resolution.Candidates, reason: gtfs.AttributionMissingVehicleID}
	}
	if !resolution.Resolved() {
		return vehicleAttribution{candidates: resolution.Candidates, reason: resolution.Reason}
	}
	agency, ok := agencyByID[resolution.AgencyID]
	if !ok {
		return vehicleAttribution{candidates: resolution.Candidates, reason: gtfs.AttributionAgencyNotReportedByOBA}
	}
	return vehicleAttribution{agency: agency, candidates: resolution.Candidates}
}

// countVehiclePositions reports how many GTFS-RT vehicle positions each agency
// on the server currently has, and returns the server-wide total.
//
// In server-mode the gauge is emitted once per OBA-reported agency from the vehicles
// attributed to it; agencies with no vehicles this tick are explicitly set to
// 0 so a series never freezes at its previous value.
//
// This count and the vehicles-for-agency count below are independent operational
// observations, not an agreement check. Their source snapshots and processing
// semantics can differ; Maglev remains authoritative for its own API behavior.
func countVehiclePositions(server models.ObaServer, agencies []models.ObaServer, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex gtfs.VehicleAttributionResolver) (int, error) {
	if realtimeStore == nil {
		err := fmt.Errorf("realtimeStore is nil for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		})
		return 0, err
	}
	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		// An absent feed reads as zero vehicles, not as a skipped tick; see the
		// equivalent guard in trackVehicleTelemetry.
		emitAgencyPositions(server, agencies, nil, utils.SanitizeServerURL(server.ObaBaseURL))
		err := fmt.Errorf("no GTFS-RT data available for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		})
		return 0, err
	}

	total := len(realtimeData.Vehicles)
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	agencyByID := agencyIndex(agencies)

	if agencyByID == nil {
		RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, serverURL).Set(float64(total))
		return total, nil
	}

	perAgency := make(map[string]int, len(agencies))
	for _, realtimeVehicle := range realtimeData.Vehicles {
		attribution := attributeVehicle(server, agencyByID, routeAgencyIndex, realtimeVehicle.Vehicle)
		if attribution.resolved() {
			perAgency[attribution.agency.AgencyID]++
		}
	}
	emitAgencyPositions(server, agencies, perAgency, serverURL)

	return total, nil
}

// emitAgencyPositions writes RealtimeVehiclePositions for every entry the pass
// covers. A nil tally emits zeros, which is what an absent feed means; an
// agency missing from a non-nil tally also reports 0, so a series cannot
// freeze at a previous tick's count.
func emitAgencyPositions(server models.ObaServer, agencies []models.ObaServer, perAgency map[string]int, serverURL string) {
	if len(agencies) == 0 {
		RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, serverURL).Set(float64(perAgency[server.AgencyID]))
		return
	}
	for _, agency := range agencies {
		RealtimeVehiclePositions.WithLabelValues(agency.AgencyID, agency.AgencyName, agency.ServerName, serverURL).Set(float64(perAgency[agency.AgencyID]))
	}
}

// countActiveVehiclesForAgency calls the OneBusAway VehiclesForAgency API for the given server,
// retrieves the list of vehicles, and reports the count to the AgencyActiveVehiclesGauge Prometheus metric.
//
// This function fetches live vehicle data from the OBA API using the agency ID.
func countActiveVehiclesForAgency(ctx context.Context, client *onebusaway.Client, server models.ObaServer) (int, error) {
	response, err := client.VehiclesForAgency.List(ctx, server.AgencyID, onebusaway.VehiclesForAgencyListParams{})

	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id":   server.AgencyID,
				"server_name": server.ServerName,
			},
		})
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	AgencyActiveVehiclesGauge.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)).Set(float64(len(response.Data.List)))
	ObaVehiclesLastSuccessfulFetch.WithLabelValues(
		server.AgencyID,
		server.AgencyName,
		server.ServerName,
		utils.SanitizeServerURL(server.ObaBaseURL),
	).Set(float64(time.Now().UTC().Unix()))

	return len(response.Data.List), nil
}

// trackVehicleTelemetry reports source timestamps, semantic state-change time,
// computed speed, and speed discrepancy for every vehicle in a successful
// GTFS-RT snapshot.
//
// See the scope-dispatch comment at the top of this file. The important
// invariant: this runs exactly once per server per tick. Transition counters
// would otherwise inflate, and vehicleLastSeen entries are keyed by the agency
// that owns the vehicle rather than by whichever agency is being iterated.
//
// In server-mode a vehicle whose route does not resolve to an agency reported by OBA, or
// which carries no vehicle ID, is counted in GtfsRtUnattributedVehicles. That
// gauge is NOT a complete reconciliation of the feed against the per-agency
// series: a vehicle that is attributable and has an ID but carries no usable
// position is dropped from the per-vehicle series here and counted by
// trackInvalidVehiclesAndStoppedOutOfBounds under
// gtfs_rt_invalid_vehicle_coordinates instead. Any query that tries to
// reconcile the two has to account for all three paths.
func trackVehicleTelemetry(server models.ObaServer, agencies []models.ObaServer, vehicleLastSeen *VehicleLastSeen, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex gtfs.VehicleAttributionResolver) error {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	serverName := server.ServerName
	now := time.Now().UTC()
	agencyByID := agencyIndex(agencies)

	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		// This is reachable before the first successful fetch. Failed fetches gate
		// this pass in both collection modes and therefore cannot process a cached
		// snapshot as a new observation.
		emitTickSummary(server, agencies, vehicleLastSeen, routeAgencyIndex, tickSummary{feedEmpty: true}, serverURL)
		err := fmt.Errorf("no GTFS-RT data available for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": serverName},
		})
		return err
	}

	if len(realtimeData.Vehicles) == 0 {
		cleanupFullDatasetVehicles(server, agencies, vehicleLastSeen, realtimeData, nil)
		// Nothing is reporting right now. Say so immediately rather than
		// coasting on last-seen entries, which ClearRoutine will not expire
		// for another hour; nothing in an empty feed can be unattributed
		// either, so that gauge zeroes with it.
		emitTickSummary(server, agencies, vehicleLastSeen, routeAgencyIndex, tickSummary{feedEmpty: true}, serverURL)
		return nil
	}

	// Build each agency's store key once. models.ServerKey re-parses the base
	// URL, which is wasted work per vehicle on a feed with thousands of them.
	agencyKeys := make(map[string]string, len(agencies)+1)
	agencyKeys[server.AgencyID] = server.ServerKey()
	for _, agency := range agencies {
		agencyKeys[agency.AgencyID] = models.ServerKey(server.ObaBaseURL, agency.AgencyID)
	}

	unattributed := 0
	reasons := make(map[gtfs.AttributionFailureReason]int, len(gtfs.AttributionFailureReasons))
	candidates := make(map[string]int)
	observedAt := realtimeData.ObservationAt
	if observedAt.IsZero() {
		observedAt = now
	}
	seen := make(map[string]map[string]bool)

	for _, realtimeVehicle := range realtimeData.Vehicles {
		vehicle := realtimeVehicle.Vehicle
		feedID := realtimeVehicle.FeedID
		attribution := attributeVehicle(server, agencyByID, routeAgencyIndex, vehicle)
		if !attribution.resolved() {
			if agencyByID != nil && vehicle.ID != nil && vehicle.ID.ID != "" {
				vehicleLastSeen.RemoveVehicleFromOtherAgencies(server, feedID, vehicle.ID.ID, "")
			}
			unattributed++
			reasons[attribution.reason]++
			for _, agencyID := range attribution.candidates {
				candidates[agencyID]++
			}
			continue
		}
		if vehicle.ID == nil || vehicle.ID.ID == "" {
			continue
		}
		vehicleID := vehicle.ID.ID
		agency := attribution.agency
		agencyKey := agencyKeys[agency.AgencyID]
		if agencyByID != nil {
			vehicleLastSeen.RemoveVehicleFromOtherAgencies(server, feedID, vehicleID, agencyKey)
		}
		prev, ok := vehicleLastSeen.Get(agencyKey, feedID, vehicleID)
		labels := []string{vehicleID, agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID}
		stateHash := realtimeVehicle.StateHash
		if stateHash == "" {
			stateHash = semanticVehicleHash(vehicle)
		}
		stateLastChanged := observedAt
		if ok {
			stateLastChanged = prev.StateLastChanged
			if stateLastChanged.IsZero() {
				stateLastChanged = observedAt
			}
			if prev.StateHash != stateHash {
				stateLastChanged = observedAt
				GtfsRtVehicleStateChanges.WithLabelValues(agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Inc()
			}
		}
		GtfsRtVehicleStateLastChangedTimestamp.WithLabelValues(labels...).Set(float64(stateLastChanged.Unix()))

		if vehicle.Timestamp != nil {
			GtfsRtVehicleSourceTimestamp.WithLabelValues(labels...).Set(float64(vehicle.Timestamp.Unix()))
			if ok && !prev.SourceTimestamp.IsZero() && vehicle.Timestamp.After(prev.SourceTimestamp) {
				GtfsRtVehicleSourceTimestampAdvances.WithLabelValues(agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Inc()
			}
		} else {
			GtfsRtVehicleSourceTimestamp.DeleteLabelValues(labels...)
		}
		if seen[agencyKey+"|"+feedID] == nil {
			seen[agencyKey+"|"+feedID] = make(map[string]bool)
		}
		seen[agencyKey+"|"+feedID][vehicleID] = true

		lat, lon, positionValid := vehicleLatLon(vehicle)
		next := prev
		if !ok {
			next = LastSeen{}
		}
		next.ObservedAt = observedAt
		next.StateHash = stateHash
		next.StateLastChanged = stateLastChanged
		next.VehicleID = vehicleID
		next.FeedID = feedID
		next.AgencyID = agency.AgencyID
		next.AgencyName = agency.AgencyName
		next.ServerName = serverName
		next.ServerURL = serverURL

		// Retain a computed speed only while the source repeats the exact same
		// valid state and remains inside its cadence-derived grace window.
		if !positionValid || vehicle.Timestamp == nil {
			deleteVehicleSpeedSeries(labels)
			next.Time = time.Time{}
			next.SourceTimestamp = time.Time{}
			next.HasPosition = false
			next.SourceInterval = 0
			next.SpeedLastComputed = time.Time{}
		} else {
			timeDelta := time.Duration(0)
			if ok && prev.HasPosition && !prev.Time.IsZero() {
				timeDelta = vehicle.Timestamp.Sub(prev.Time)
			}
			switch {
			case timeDelta > 0:
				distance := geo.HaversineDistance(prev.Lat, prev.Lon, lat, lon)
				computedSpeed := distance / timeDelta.Seconds()
				VehicleSpeedGauge.WithLabelValues(labels...).Set(computedSpeed)
				VehicleSpeedDiscrepancyRatioGauge.DeleteLabelValues(labels...)
				if vehicle.Position.Speed != nil && *vehicle.Position.Speed > 0 {
					reportedSpeed := float64(*vehicle.Position.Speed)
					VehicleSpeedDiscrepancyRatioGauge.WithLabelValues(labels...).Set(math.Abs(computedSpeed-reportedSpeed) / reportedSpeed)
				}
				next.SourceInterval = timeDelta
				next.SpeedLastComputed = observedAt
				GtfsRtVehicleSpeedLastComputedTimestamp.WithLabelValues(labels...).Set(float64(observedAt.Unix()))
			case timeDelta == 0 && ok && prev.HasPosition && prev.StateHash == stateHash && !prev.SpeedLastComputed.IsZero():
				if observedAt.Sub(prev.SpeedLastComputed) > vehicleSpeedGrace(vehicleLastSeen.CollectionInterval(), prev.SourceInterval) {
					deleteVehicleSpeedSeries(labels)
					next.SpeedLastComputed = time.Time{}
				}
			default:
				deleteVehicleSpeedSeries(labels)
				next.SourceInterval = 0
				next.SpeedLastComputed = time.Time{}
			}
			next.Time = *vehicle.Timestamp
			next.Lat = lat
			next.Lon = lon
			next.HasPosition = true
			next.SourceTimestamp = *vehicle.Timestamp
		}
		vehicleLastSeen.Set(agencyKey, feedID, vehicleID, next)
	}
	cleanupFullDatasetVehicles(server, agencies, vehicleLastSeen, realtimeData, seen)

	emitTickSummary(server, agencies, vehicleLastSeen, routeAgencyIndex, tickSummary{unattributed: unattributed, reasons: reasons, candidates: candidates}, serverURL)

	return nil
}

func vehicleSpeedGrace(collectionInterval, sourceInterval time.Duration) time.Duration {
	grace := 3 * collectionInterval
	if sourceGrace := 2 * sourceInterval; sourceGrace > grace {
		grace = sourceGrace
	}
	return min(max(grace, time.Minute), 5*time.Minute)
}

func deleteVehicleSpeedSeries(labels []string) {
	VehicleSpeedGauge.DeleteLabelValues(labels...)
	VehicleSpeedDiscrepancyRatioGauge.DeleteLabelValues(labels...)
	GtfsRtVehicleSpeedLastComputedTimestamp.DeleteLabelValues(labels...)
}

func semanticVehicleHash(vehicle remoteGtfs.Vehicle) string {
	vehicle.Timestamp = nil
	data, _ := json.Marshal(vehicle)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cleanupFullDatasetVehicles(server models.ObaServer, agencies []models.ObaServer, store *VehicleLastSeen, data *models.RealtimeData, seen map[string]map[string]bool) {
	if len(agencies) > 0 {
		for _, feed := range data.Feeds {
			if feed.FullDataset {
				store.RemoveMissingForServer(server, feed.FeedID, seen)
			}
		}
		return
	}
	keys := []string{server.ServerKey()}
	for _, feed := range data.Feeds {
		if !feed.FullDataset {
			continue
		}
		for _, key := range keys {
			store.RemoveMissing(key, feed.FeedID, seen[key+"|"+feed.FeedID])
		}
	}
}

// tickSummary carries the per-tick facts emitTickSummary needs beyond the
// last-seen store. feedEmpty is stated rather than signalled by a nil store,
// so "the feed reported nothing" cannot be confused with "there is no store".
type tickSummary struct {
	unattributed int
	reasons      map[gtfs.AttributionFailureReason]int
	candidates   map[string]int
	feedEmpty    bool
}

// emitTickSummary publishes every gauge trackVehicleTelemetry owes per tick,
// for both the empty-feed and the populated-feed path. Both paths funnel
// through here on purpose: each of these gauges must be written on EVERY tick
// or it freezes at its last value, and a second exit that emitted only some of
// them is exactly how gtfs_rt_unattributed_vehicles came to sit pinned at a
// stale count while every other vehicle metric correctly dropped to 0.
//
// TrackedVehiclesGauge is emitted for every agency the pass covers, reading
// each agency's own last-seen slot; agencies with nothing tracked report 0
// rather than retaining a stale value.
//
// GtfsRtUnattributedVehicles is server-scoped and meaningless for a
// single-agency entry, so agency-mode publishes no series for it at all. See
// the scope-dispatch comment at the top of this file.
func emitTickSummary(server models.ObaServer, agencies []models.ObaServer, vehicleLastSeen *VehicleLastSeen, routeAgencyIndex gtfs.VehicleAttributionResolver, summary tickSummary, serverURL string) {
	count := func(serverKey string) float64 {
		if summary.feedEmpty {
			return 0
		}
		return float64(vehicleLastSeen.Count(serverKey))
	}

	if len(agencies) == 0 {
		TrackedVehiclesGauge.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, serverURL).
			Set(count(server.ServerKey()))
		return
	}
	for _, agency := range agencies {
		TrackedVehiclesGauge.WithLabelValues(agency.AgencyID, agency.AgencyName, agency.ServerName, serverURL).
			Set(count(models.ServerKey(server.ObaBaseURL, agency.AgencyID)))
	}
	serverLabels := prometheus.Labels{"server_url": serverURL}
	GtfsRtUnattributedVehicles.DeletePartialMatch(serverLabels)
	GtfsRtUnattributedVehiclesByReason.DeletePartialMatch(serverLabels)
	GtfsRtUnattributedVehicleCandidateAssociations.DeletePartialMatch(serverLabels)
	GtfsRtUnattributedVehicles.WithLabelValues(server.ServerName, serverURL).Set(float64(summary.unattributed))
	for _, reason := range gtfs.AttributionFailureReasons {
		GtfsRtUnattributedVehiclesByReason.WithLabelValues(server.ServerName, serverURL, string(reason)).Set(float64(summary.reasons[reason]))
	}
	if routeAgencyIndex != nil {
		for agencyID, agencyName := range routeAgencyIndex.AgencyNames(server.ServerKey()) {
			GtfsRtUnattributedVehicleCandidateAssociations.WithLabelValues(agencyID, agencyName, server.ServerName, serverURL).Set(float64(summary.candidates[agencyID]))
		}
	}
}

// VehicleStatusStoppedAtStop represents the GTFS-realtime vehicle stop status
// where the vehicle is currently stopped at the stop.
//
// Possible values for VehicleStopStatus are:
//   - 0 (INCOMING_AT): Vehicle is about to arrive at the stop
//   - 1 (STOPPED_AT): Vehicle is standing at a stop (this constant)
//   - 2 (IN_TRANSIT_TO): Vehicle has departed and is in transit to the next stop
//
// These values correspond to the VehicleStopStatus enum defined in the GTFS-realtime specification.
//
// For more details, see:
// https://gtfs.org/documentation/realtime/reference/#enum-vehiclestopstatus
const VehicleStatusStoppedAtStop = 1

// trackInvalidVehiclesAndStoppedOutOfBounds collects and reports metrics related
// to vehicle position validity:
//  1. Invalid coordinate check: vehicles with missing or out-of-range lat/lon.
//  2. Bounding box check: vehicles that are *stopped at a stop* but located
//     outside the bounding box derived from the static GTFS stops.
//
// Both counts are attributed per agency (see the scope-dispatch comment at the
// top of this file), and agencies with nothing to report are set to 0.
//
// Coordinate validity is judged BEFORE attribution, and vehicles that cannot
// be attributed are counted under the server-scoped entry (empty agency_id /
// agency_name in server-mode) rather than dropped. Attributing first would
// hide the malformed entities these gauges exist to surface — an entity with
// no TripDescriptor and no position has no route_id to attribute with — and
// would break the invariant that
// `sum by (server_url) (gtfs_rt_invalid_vehicle_coordinates)` equals the
// server-wide count.
func trackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer, agencies []models.ObaServer, boundingBoxStore *geo.BoundingBoxStore, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex gtfs.VehicleAttributionResolver) error {
	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		err := fmt.Errorf("no GTFS-RT data available for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		})
		return err
	}

	serverBox, ok := boundingBoxStore.Get(server.ServerKey())
	if !ok {
		return fmt.Errorf("no bounding box found for server key %s", server.ServerKey())
	}

	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	agencyByID := agencyIndex(agencies)
	agencyBoxes := make(map[string]geo.BoundingBox, len(agencies))
	for _, agency := range agencies {
		if bbox, ok := boundingBoxStore.Get(models.ServerKey(server.ObaBaseURL, agency.AgencyID)); ok {
			agencyBoxes[agency.AgencyID] = bbox
		}
	}

	invalid := make(map[string]int, len(agencies)+1)
	outOfBounds := make(map[string]int, len(agencies)+1)

	for _, realtimeVehicle := range realtimeData.Vehicles {
		v := realtimeVehicle.Vehicle

		// Judge the coordinates BEFORE attribution. The most malformed
		// entities — no TripDescriptor, so no route_id to resolve, and no
		// position — are exactly the ones attribution cannot place, and
		// exactly the ones this gauge exists to catch. Attributing first and
		// skipping the failures would make the worst feed data invisible.
		lat, lon, coordsValid := vehicleLatLon(v)

		// Vehicles that cannot be placed with an agency fall to the
		// server-scoped entry, whose agency labels are empty in server-mode.
		// That keeps sum by (server_url) equal to the server-wide count.
		bucket := server.AgencyID
		boundingBox := serverBox
		attribution := attributeVehicle(server, agencyByID, routeAgencyIndex, v)
		if attribution.resolved() {
			bucket = attribution.agency.AgencyID
			if agencyBox, ok := agencyBoxes[attribution.agency.AgencyID]; ok {
				boundingBox = agencyBox
			}
		}

		if !coordsValid {
			invalid[bucket]++
			continue
		}

		// Check bounding box only if vehicle is stopped at the stop
		if v.CurrentStatus != nil && *v.CurrentStatus == VehicleStatusStoppedAtStop {
			if !boundingBox.Contains(lat, lon) {
				outOfBounds[bucket]++
			}
		}
	}

	emit := func(entry models.ObaServer) {
		InvalidVehicleCoordinatesGauge.WithLabelValues(entry.AgencyID, entry.AgencyName, entry.ServerName, serverURL).Set(float64(invalid[entry.AgencyID]))
		StoppedOutOfBoundsVehiclesGauge.WithLabelValues(entry.AgencyID, entry.AgencyName, entry.ServerName, serverURL).Set(float64(outOfBounds[entry.AgencyID]))
	}

	if agencyByID == nil {
		emit(server)
		return nil
	}
	for _, agency := range agencies {
		emit(agency)
	}
	// The server-scoped catch-all is emitted unconditionally, including its
	// zero, so it can never freeze at a stale count once the bad vehicles
	// leave the feed.
	emit(server)

	return nil
}

// vehicleLatLon returns a vehicle's position and whether it is usable: a
// vehicle with no position, a half-populated one, or coordinates outside the
// WGS-84 range reports false.
func vehicleLatLon(v remoteGtfs.Vehicle) (lat, lon float64, valid bool) {
	if v.Position == nil || v.Position.Latitude == nil || v.Position.Longitude == nil {
		return 0, 0, false
	}
	lat = float64(*v.Position.Latitude)
	lon = float64(*v.Position.Longitude)
	return lat, lon, geo.IsValidLatLon(lat, lon)
}
