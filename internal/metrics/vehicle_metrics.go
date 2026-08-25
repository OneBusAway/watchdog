package metrics

import (
	"context"
	"fmt"
	"math"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// countVehiclePositions returns the number of vehicles present in the GTFS-RT feed
// for a given server, as stored in the provided RealtimeStore.
//
// It retrieves the GTFS-RT data for the server and reports the vehicle count to
// the RealtimeVehiclePositions Prometheus metric.
func countVehiclePositions(server models.ObaServer, realtimeStore *gtfs.RealtimeStore) (int, error) {
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
	count := len(realtimeData.Vehicles)

	RealtimeVehiclePositions.WithLabelValues(server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)).Set(float64(count))

	return count, nil
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

// trackVehicleTelemetry collects and reports various telemetry metrics for vehicles in a GTFS-RT feed.
//
// The dispatch between agency-mode and server-mode is controlled by whether
// routeAgencyIndex is nil: nil means agency-mode (one agency per entry, the
// function trusts server.AgencyID for every vehicle), non-nil means
// server-mode (route_id → agency_id lookup).
//
// In agency-mode this runs once over every vehicle in the merged realtime
// store, attributing each vehicle to the configured agency (server.AgencyID)
// because the feed is per-agency.
//
// In server-mode the realtime feed covers multiple agencies; we must attribute
// each vehicle by looking up its TripDescriptor.route_id in the
// RouteAgencyIndex. Vehicles whose route_id is empty or unknown are skipped
// and counted in GtfsRtUnattributedVehicles so operators can detect static
// feeds that don't cover every RT route.
func trackVehicleTelemetry(server models.ObaServer, vehicleLastSeen *VehicleLastSeen, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex *gtfs.RouteAgencyIndex) error {
	agencyID := server.AgencyID
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	serverName := server.ServerName
	now := time.Now().UTC()

	realtimeData := realtimeStore.Get(server.ServerKey())
	if realtimeData == nil {
		err := fmt.Errorf("no GTFS-RT data available for agency %s", agencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": agencyID, "server_name": serverName},
		})
		return err
	}

	if len(realtimeData.Vehicles) == 0 {
		TrackedVehiclesGauge.WithLabelValues(agencyID, server.AgencyName, serverName, serverURL).Set(0)
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

		// Attribute the vehicle to an agency. In agency-mode the index is not
		// consulted; we trust server.AgencyID. In server-mode we look up
		// route_id → agency_id and skip vehicles whose route we cannot
		// attribute.
		attributedAgencyID := agencyID
		if routeAgencyIndex != nil && vehicle.Trip != nil {
			routeID := vehicle.Trip.ID.RouteID
			if id, ok := routeAgencyIndex.Get(server.ObaBaseURL, routeID); ok && id != "" {
				attributedAgencyID = id
			} else if routeID != "" {
				unattributed++
				continue
			}
		}

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
		VehicleReportCount.WithLabelValues(vehicleID, attributedAgencyID, server.AgencyName, serverName, serverURL, feedID).Inc()
		VehicleReportInterval.WithLabelValues(vehicleID, attributedAgencyID, server.AgencyName, serverName, serverURL, feedID).Set(interval)

		// Compute speed
		prev, ok := vehicleLastSeen.Get(server.ServerKey(), feedID, vehicleID)
		if ok {
			timeDelta := seenAt.Sub(prev.Time).Seconds()
			if timeDelta > 0 {
				distance := geo.HaversineDistance(prev.Lat, prev.Lon, lat, lon)
				computedSpeed := distance / timeDelta

				VehicleSpeedGauge.WithLabelValues(vehicleID, attributedAgencyID, server.AgencyName, serverName, serverURL, feedID).Set(computedSpeed)

				if vehicle.Position.Speed != nil {
					reportedSpeed := float64(*vehicle.Position.Speed)
					if reportedSpeed > 0 {
						diffRatio := math.Abs(computedSpeed-reportedSpeed) / reportedSpeed
						VehicleSpeedDiscrepancyRatioGauge.WithLabelValues(vehicleID, attributedAgencyID, server.AgencyName, serverName, serverURL, feedID).Set(diffRatio)
					}
				}
			}
		}

		vehicleLastSeen.Set(server.ServerKey(), feedID, vehicleID, LastSeen{
			Time: seenAt,
			Lat:  lat,
			Lon:  lon,
		})
	}

	if routeAgencyIndex != nil {
		GtfsRtUnattributedVehicles.WithLabelValues(serverName, serverURL).Set(float64(unattributed))
	}

	TrackedVehiclesGauge.WithLabelValues(agencyID, server.AgencyName, serverName, serverURL).Set(float64(vehicleLastSeen.Count(server.ServerKey())))

	return nil
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
// to vehicle position validity. Like trackVehicleTelemetry, it attributes
// vehicles to agencies via the RouteAgencyIndex in server-mode and skips
// vehicles whose route_id is unattributable.
func trackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer, boundingBoxStore *geo.BoundingBoxStore, realtimeStore *gtfs.RealtimeStore, routeAgencyIndex *gtfs.RouteAgencyIndex) error {
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
	serverName := server.ServerName

	invalidCount := 0
	outOfBoundsCount := 0

	for _, realtimeVehicle := range realtimeData.Vehicles {
		v := realtimeVehicle.Vehicle
		if v.Position == nil || v.Position.Latitude == nil || v.Position.Longitude == nil {
			invalidCount++
			continue
		}

		lat := float64(*v.Position.Latitude)
		lon := float64(*v.Position.Longitude)

		if !geo.IsValidLatLon(lat, lon) {
			invalidCount++
			continue
		}

		// Check bounding box only if vehicle is stopped at the stop
		if v.CurrentStatus != nil && *v.CurrentStatus == VehicleStatusStoppedAtStop {
			if !boundingBox.Contains(lat, lon) {
				outOfBoundsCount++
			}
		}
	}

	InvalidVehicleCoordinatesGauge.WithLabelValues(server.AgencyID, server.AgencyName, serverName, serverURL).Set(float64(invalidCount))
	StoppedOutOfBoundsVehiclesGauge.WithLabelValues(server.AgencyID, server.AgencyName, serverName, serverURL).Set(float64(outOfBoundsCount))

	return nil
}
