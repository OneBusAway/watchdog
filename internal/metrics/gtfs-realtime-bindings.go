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
