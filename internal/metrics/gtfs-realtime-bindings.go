package metrics

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	remoteGtfs "github.com/jamespfennell/gtfs"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func CountVehiclePositions(server models.ObaServer) (int, error) {
	resp, err := gtfs.FetchGTFSRTFeed(server)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("failed to read GTFS-RT feed: %v", err)
		report.ReportError(err)
		return 0, err
	}

	realtimeData, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS-RT feed: %v", err)
		report.ReportError(err)
		return 0, err
	}

	count := len(realtimeData.Vehicles)

	RealtimeVehiclePositions.WithLabelValues(
		server.VehiclePositionUrl,
		strconv.Itoa(server.ID),
	).Set(float64(count))

	return count, nil
}

func VehiclesForAgencyAPI(server models.ObaServer) (int, error) {

	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.VehiclesForAgency.List(ctx, server.AgencyID, onebusaway.VehiclesForAgencyListParams{})

	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": strconv.Itoa(server.ID),
				"agency_id": server.AgencyID,
			},
		})
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	VehicleCountAPI.WithLabelValues(server.AgencyID, strconv.Itoa(server.ID)).Set(float64(len(response.Data.List)))

	return len(response.Data.List), nil
}

func CheckVehicleCountMatch(server models.ObaServer) error {
	gtfsRtVehicleCount, err := CountVehiclePositions(server)
	if err != nil {
		err := fmt.Errorf("failed to count vehicle positions from GTFS-RT: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
		})
		return err
	}

	apiVehicleCount, err := VehiclesForAgencyAPI(server)
	if err != nil {
		err := fmt.Errorf("failed to count vehicle positions from API: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
		})
		return err
	}

	match := 0
	if gtfsRtVehicleCount == apiVehicleCount {
		match = 1
	}

	VehicleCountMatch.WithLabelValues(server.AgencyID, strconv.Itoa(server.ID)).Set(float64(match))

	return nil
}

// vehicleLastSeen keeps track of the last seen time for each vehicle by server ID
var vehicleLastSeen = make(map[int]map[string]time.Time)

// TrackVehicleReportingFrequency fetches the GTFS-RT feed and updates the reporting frequency of vehicles
// It updates the VehicleReportInterval and VehicleMessageCount metrics for each vehicle
// It also updates the vehicleLastSeen map to track when each vehicle was last seen
// If a vehicle's last seen time is not available, it uses the current time
func TrackVehicleReportingFrequency(server models.ObaServer) error {
	resp, err := gtfs.FetchGTFSRTFeed(server)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		report.ReportError(err)
		return err
	}

	realtimeData, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		report.ReportError(err)
		return err
	}

	serverID := server.ID
	if vehicleLastSeen[serverID] == nil {
		vehicleLastSeen[serverID] = make(map[string]time.Time)
	}

	now := time.Now()

	for _, vehicle := range realtimeData.Vehicles {
		if vehicle.ID == nil || vehicle.ID.ID == "" {
			continue
		}
		vehicleID := vehicle.ID.ID

		seenAt := now
		if vehicle.Timestamp != nil {
			seenAt = *vehicle.Timestamp
		}

		vehicleLastSeen[serverID][vehicleID] = seenAt
		interval := now.Sub(seenAt).Seconds()

		VehicleReportCount.WithLabelValues(vehicleID, strconv.Itoa(serverID)).Inc()
		VehicleReportInterval.WithLabelValues(vehicleID, strconv.Itoa(serverID)).Set(interval)
	}

	return nil
}

// VehicleStatusStoppedAtStop represents the GTFS-realtime vehicle stop status
// where the vehicle is currently stopped at the stop.
//
// Possible values for VehicleStopStatus are:
//   - 0 (INCOMING_AT): Vehicle is about to arrive at the stop
//   - 1 (STOPPED_AT): Vehicle is standing at the stop (this constant)
//   - 2 (IN_TRANSIT_TO): Vehicle has departed and is in transit to the next stop
//
// These values correspond to the VehicleStopStatus enum defined in the GTFS-realtime specification.
//
// For more details, see:
// https://gtfs.org/documentation/realtime/reference/#enum-vehiclestopstatus
const VehicleStatusStoppedAtStop = 1

// TrackInvalidVehiclesAndStoppedOutOfBounds collects and reports metrics related to vehicle position validity.
//
// It performs two checks on each vehicle in the GTFS-RT feed:
//  1. Invalid coordinate check: counts vehicles with missing or out-of-range latitude/longitude.
//  2. Bounding box check: counts vehicles that are *stopped at a stop* but located outside the bounding box.
//
// Bounding box validation is only applied when the vehicle status is STOPPED_AT (i.e., it is currently at a stop).
// This is because the bounding box is derived from the static GTFS stops, not the full operating area of the vehicle.
// A vehicle moving between stops may legitimately report positions outside this bounding box.
// However, if a vehicle reports being *at a stop* that lies outside the bounding box built from known static stops,
// it indicates a potential data issue (e.g., an unknown or misplaced stop).
//
// The results are exposed via Prometheus metrics:
// - InvalidVehicleCoordinatesGauge: for invalid or missing coordinates
// - StoppedOutOfBoundsVehiclesGauge: for vehicles stopped outside the bounding box
func TrackInvalidVehiclesAndStoppedOutOfBounds(server models.ObaServer, store *geo.BoundingBoxStore) error {
	resp, err := gtfs.FetchGTFSRTFeed(server)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		report.ReportError(err)
		return err
	}

	realtimeData, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		report.ReportError(err)
		return err
	}

	boundingBox, ok := store.Get(server.ID)
	if !ok {
		return fmt.Errorf("no bounding box found for server ID %d", server.ID)
	}

	invalidCount := 0
	outOfBoundsCount := 0

	for _, v := range realtimeData.Vehicles {
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

	serverID := strconv.Itoa(server.ID)
	InvalidVehicleCoordinatesGauge.WithLabelValues(serverID).Set(float64(invalidCount))
	StoppedOutOfBoundsVehiclesGauge.WithLabelValues(serverID).Set(float64(outOfBoundsCount))

	return nil
}
