package metrics

import (
	"context"
	"fmt"
	"math"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	onebusaway "github.com/OneBusAway/go-sdk"
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
//   - nil (agency-mode): the configured entry names a single agency, so every
//     vehicle in the feed belongs to it. The route → agency index is NOT
//     consulted — the operator already told us the answer, and consulting the
//     index would silently drop every vehicle whenever the static bundle
//     failed to download.
//   - non-nil (server-mode): the entry is server-scoped and the feed is the
//     merged feed for every agency the server reports. Each vehicle is
//     attributed to an agency through its TripDescriptor.route_id, and the
//     pass runs ONCE per server per tick — not once per agency. Running it per
//     agency would multiply the VehicleReportCount counter by the agency count
//     and file every vehicle under every agency's last-seen slot.
//
// In server-mode the realtime feed and the bounding box are both read from the
// server-scoped key (models.ServerKey(oba_base_url, "")), which is where
// collectForServerScope registers the once-per-tick fetch.

// agencyIndex keys the live agency entries by agency_id so attribution is an
// O(1) lookup. Returns nil for agency-mode, which every pass treats as "trust
// server.AgencyID".
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
// In agency-mode (agencyByID == nil) the answer is always the configured
// entry. In server-mode the vehicle's route_id is resolved through the route →
// agency index and matched against the live agency set. A vehicle whose trip
// carries no route_id, whose route is unknown to the index, or whose route
// belongs to an agency the server is not currently reporting cannot be
// attributed; the caller counts it in GtfsRtUnattributedVehicles.
func attributeVehicle(server models.ObaServer, agencyByID map[string]models.ObaServer, routeAgencyIndex *gtfs.RouteAgencyIndex, vehicle remoteGtfs.Vehicle) (models.ObaServer, bool) {
	if agencyByID == nil {
		return server, true
	}
	if vehicle.Trip == nil || routeAgencyIndex == nil {
		return models.ObaServer{}, false
	}
	agencyID, ok := routeAgencyIndex.Get(server.ObaBaseURL, vehicle.Trip.ID.RouteID)
	if !ok || agencyID == "" {
		return models.ObaServer{}, false
	}
	agency, ok := agencyByID[agencyID]
	return agency, ok
}

// countVehiclePositions reports how many GTFS-RT vehicle positions each agency
// on the server currently has, and returns the server-wide total.
//
// In server-mode the gauge is emitted once per live agency from the vehicles
// attributed to it; agencies with no vehicles this tick are explicitly set to
// 0 so a series never freezes at its previous value.
func countVehiclePositions(server models.ObaServer, agencies []models.ObaServer, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex *gtfs.RouteAgencyIndex) (int, error) {
	if realtimeStore == nil {
		err := fmt.Errorf("realtimeStore is nil for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		})
		return 0, err
	}
	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
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
		if agency, ok := attributeVehicle(server, agencyByID, routeAgencyIndex, realtimeVehicle.Vehicle); ok {
			perAgency[agency.AgencyID]++
		}
	}
	for _, agency := range agencies {
		RealtimeVehiclePositions.WithLabelValues(agency.AgencyID, agency.AgencyName, agency.ServerName, serverURL).Set(float64(perAgency[agency.AgencyID]))
	}

	return total, nil
}

// countActiveVehiclesForAgency calls the OneBusAway VehiclesForAgency API for the given server,
// retrieves the list of vehicles, and reports the count to the AgencyActiveVehiclesGauge Prometheus metric.
//
// This function fetches live vehicle data from the OBA API using the agency ID.
func countActiveVehiclesForAgency(client *onebusaway.Client, server models.ObaServer) (int, error) {
	ctx := context.Background()

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

	return len(response.Data.List), nil
}

// trackVehicleTelemetry collects and reports per-vehicle telemetry — report
// count, reporting interval, computed speed, and the discrepancy against the
// speed the feed reports — for every vehicle in the GTFS-RT feed.
//
// See the scope-dispatch comment at the top of this file. The important
// invariant: this runs exactly once per server per tick. VehicleReportCount is
// a Counter, so a second pass over the same feed within one tick would
// permanently inflate it, and vehicleLastSeen entries are keyed by the agency
// that owns the vehicle rather than by whichever agency is being iterated.
func trackVehicleTelemetry(server models.ObaServer, agencies []models.ObaServer, vehicleLastSeen *VehicleLastSeen, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex *gtfs.RouteAgencyIndex) error {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	serverName := server.ServerName
	now := time.Now().UTC()
	agencyByID := agencyIndex(agencies)

	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		err := fmt.Errorf("no GTFS-RT data available for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": serverName},
		})
		return err
	}

	if len(realtimeData.Vehicles) == 0 {
		// Nothing is reporting right now. Say so immediately rather than
		// coasting on last-seen entries, which ClearRoutine will not expire
		// for another hour.
		setTrackedVehicles(server, agencies, nil, serverURL)
		return nil
	}

	unattributed := 0

	for _, realtimeVehicle := range realtimeData.Vehicles {
		vehicle := realtimeVehicle.Vehicle
		feedID := realtimeVehicle.FeedID
		if vehicle.ID == nil || vehicle.ID.ID == "" {
			continue
		}
		vehicleID := vehicle.ID.ID

		agency, ok := attributeVehicle(server, agencyByID, routeAgencyIndex, vehicle)
		if !ok {
			unattributed++
			continue
		}
		agencyKey := models.ServerKey(server.ObaBaseURL, agency.AgencyID)

		if vehicle.Position == nil || vehicle.Position.Latitude == nil || vehicle.Position.Longitude == nil {
			continue
		}
		lat := float64(*vehicle.Position.Latitude)
		lon := float64(*vehicle.Position.Longitude)

		seenAt := now
		if vehicle.Timestamp != nil {
			seenAt = *vehicle.Timestamp
		}

		interval := now.Sub(seenAt).Seconds()
		VehicleReportCount.WithLabelValues(vehicleID, agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Inc()
		VehicleReportInterval.WithLabelValues(vehicleID, agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Set(interval)

		// Compute speed
		prev, ok := vehicleLastSeen.Get(agencyKey, feedID, vehicleID)
		if ok {
			timeDelta := seenAt.Sub(prev.Time).Seconds()
			if timeDelta > 0 {
				distance := geo.HaversineDistance(prev.Lat, prev.Lon, lat, lon)
				computedSpeed := distance / timeDelta

				VehicleSpeedGauge.WithLabelValues(vehicleID, agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Set(computedSpeed)

				if vehicle.Position.Speed != nil {
					reportedSpeed := float64(*vehicle.Position.Speed)
					if reportedSpeed > 0 {
						diffRatio := math.Abs(computedSpeed-reportedSpeed) / reportedSpeed
						VehicleSpeedDiscrepancyRatioGauge.WithLabelValues(vehicleID, agency.AgencyID, agency.AgencyName, serverName, serverURL, feedID).Set(diffRatio)
					}
				}
			}
		}

		vehicleLastSeen.Set(agencyKey, feedID, vehicleID, LastSeen{
			Time: seenAt,
			Lat:  lat,
			Lon:  lon,
		})
	}

	if agencyByID != nil {
		GtfsRtUnattributedVehicles.WithLabelValues(serverName, serverURL).Set(float64(unattributed))
	}

	setTrackedVehicles(server, agencies, vehicleLastSeen, serverURL)

	return nil
}

// setTrackedVehicles emits TrackedVehiclesGauge for every agency the pass
// covers, reading each agency's own last-seen slot. Agencies with nothing
// tracked report 0 rather than retaining a stale value.
//
// A nil vehicleLastSeen means "report zero for everyone" — the empty-feed
// case, where the store's contents are not evidence that anything is still
// being tracked.
func setTrackedVehicles(server models.ObaServer, agencies []models.ObaServer, vehicleLastSeen *VehicleLastSeen, serverURL string) {
	count := func(serverKey string) float64 {
		if vehicleLastSeen == nil {
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
// The bounding box itself is still server-wide: it is computed over the union
// of every static feed's stops, so a vehicle stopped in agency A's territory is
// validated against a rectangle covering every agency on the server. Scoping
// the box per agency needs the storage-shape change described in
// TODO(scoped-store) in config/scoping.go; until then, treat
// gtfs_rt_stopped_out_of_bounds_vehicles as a loose bound.
func trackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer, agencies []models.ObaServer, boundingBoxStore *geo.BoundingBoxStore, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex *gtfs.RouteAgencyIndex) error {
	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		err := fmt.Errorf("no GTFS-RT data available for agency %s", server.AgencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
		})
		return err
	}

	boundingBox, ok := boundingBoxStore.Get(server.ServerKey())
	if !ok {
		return fmt.Errorf("no bounding box found for server key %s", server.ServerKey())
	}

	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	agencyByID := agencyIndex(agencies)

	invalid := make(map[string]int, len(agencies)+1)
	outOfBounds := make(map[string]int, len(agencies)+1)

	for _, realtimeVehicle := range realtimeData.Vehicles {
		v := realtimeVehicle.Vehicle

		agency, attributed := attributeVehicle(server, agencyByID, routeAgencyIndex, v)
		if !attributed {
			continue
		}

		if v.Position == nil || v.Position.Latitude == nil || v.Position.Longitude == nil {
			invalid[agency.AgencyID]++
			continue
		}

		lat := float64(*v.Position.Latitude)
		lon := float64(*v.Position.Longitude)

		if !geo.IsValidLatLon(lat, lon) {
			invalid[agency.AgencyID]++
			continue
		}

		// Check bounding box only if vehicle is stopped at the stop
		if v.CurrentStatus != nil && *v.CurrentStatus == VehicleStatusStoppedAtStop {
			if !boundingBox.Contains(lat, lon) {
				outOfBounds[agency.AgencyID]++
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

	return nil
}
