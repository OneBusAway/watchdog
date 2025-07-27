package metrics

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
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

// vehicleLastSeen stores timestamp & coordinates for speed computation
type LastSeen struct {
	Time time.Time
	Lat  float64
	Lon  float64
}

// VehicleLastSeen stores the most recent known location and timestamp for each vehicle per server.
//
// The outer map key is the server ID (int), and the inner map key is the vehicle ID (string).
// Each entry stores a `LastSeen` struct containing the last known latitude, longitude, and timestamp.
//
// This cache is used to:
//   - Compute the distance between successive vehicle locations.
//   - Estimate vehicle speed based on elapsed time between updates.
//   - Detect anomalies in vehicle movement patterns (e.g., unrealistic jumps).

type VehicleLastSeen struct {
	Mu    sync.RWMutex
	Store map[int]map[string]LastSeen
}

func NewVehicleLastSeen() *VehicleLastSeen {
	return &VehicleLastSeen{
		Store: make(map[int]map[string]LastSeen),
	}
}

func (vehicleLastSeen *VehicleLastSeen) Get(serverID int, vehicleID string) (LastSeen, bool) {
	vehicleLastSeen.Mu.RLock()
	defer vehicleLastSeen.Mu.RUnlock()

	if vehicles, ok := vehicleLastSeen.Store[serverID]; ok {
		lastSeen, ok := vehicles[vehicleID]
		return lastSeen, ok
	}
	return LastSeen{}, false
}

func (vehicleLastSeen *VehicleLastSeen) Set(serverID int, vehicleID string, lastSeen LastSeen) {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()

	if _, ok := vehicleLastSeen.Store[serverID]; !ok {
		vehicleLastSeen.Store[serverID] = make(map[string]LastSeen)
	}
	vehicleLastSeen.Store[serverID][vehicleID] = lastSeen
}

func (v *VehicleLastSeen) Count(serverID int) int {
	v.Mu.RLock()
	defer v.Mu.RUnlock()

	return len(v.Store[serverID])
}

func (vehicleLastSeen *VehicleLastSeen) ClearRoutine(ctx context.Context, timeInterval, threshold time.Duration) {
	ticker := time.NewTicker(timeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			vehicleLastSeen.clear(threshold)
		case <-ctx.Done():
			return
		}
	}
}

func (vehicleLastSeen *VehicleLastSeen) clear(threshold time.Duration) {
	vehicleLastSeen.Mu.Lock()
	defer vehicleLastSeen.Mu.Unlock()

	if len(vehicleLastSeen.Store) == 0 {
		return
	}

	now := time.Now().UTC()

	for serverID, vehicles := range vehicleLastSeen.Store {

		for vehicleID, lastSeen := range vehicles {
			if lastSeen.Time.Before(now) && now.Sub(lastSeen.Time) > threshold {
				delete(vehicleLastSeen.Store[serverID], vehicleID)
			}
		}

		if len(vehicleLastSeen.Store[serverID]) == 0 {
			delete(vehicleLastSeen.Store, serverID)
		}

	}
}

// TrackVehicleTelemetry collects and reports various telemetry metrics for vehicles in a GTFS-RT feed.
//
// This function performs the following tasks:
//  1. Fetches and parses the GTFS-RT vehicle positions feed for the given OBA server.
//  2. For each valid vehicle entry:
//     - Tracks the number of GTFS-RT updates received (`vehicle_report_total`).
//     - Measures the interval since the last report (`vehicle_position_report_interval_seconds`).
//     - Computes the vehicle speed based on current and previous coordinates and timestamps.
//     - Reports the computed speed to Prometheus (`gtfs_rt_vehicle_computed_speed`).
//     - Compares the computed speed with the reported speed (if available) and reports the relative discrepancy
//     (`gtfs_rt_vehicle_speed_discrepancy_ratio`).
//
// All metrics are labeled by `vehicle_id`, `server_id`, and `agency_id` to support detailed monitoring and alerting.
//
// The function maintains a local in-memory store (`vehicleLastSeen`) to cache the last known location and timestamp
// for each vehicle per server.
//
// Parameters:
//   - server: the `ObaServer` instance representing the target OBA server.
//
// Returns:
//   - An error if the feed cannot be fetched or parsed, otherwise nil.
func TrackVehicleTelemetry(server models.ObaServer, vehicleLastSeen *VehicleLastSeen) error {
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
	agencyID := server.AgencyID

	now := time.Now().UTC()

	for _, vehicle := range realtimeData.Vehicles {
		if vehicle.ID == nil || vehicle.ID.ID == "" {
			continue
		}
		vehicleID := vehicle.ID.ID

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
		VehicleReportCount.WithLabelValues(vehicleID, strconv.Itoa(serverID)).Inc()
		VehicleReportInterval.WithLabelValues(vehicleID, strconv.Itoa(serverID)).Set(interval)

		// Compute speed
		prev, ok := vehicleLastSeen.Get(serverID, vehicleID)
		if ok {
			timeDelta := seenAt.Sub(prev.Time).Seconds()
			if timeDelta > 0 {
				distance := geo.HaversineDistance(prev.Lat, prev.Lon, lat, lon)
				computedSpeed := distance / timeDelta

				VehicleSpeedGauge.WithLabelValues(vehicleID, agencyID, strconv.Itoa(serverID)).Set(computedSpeed)

				// Compare reported speed with computed speed
				if vehicle.Position.Speed != nil {
					reportedSpeed := float64(*vehicle.Position.Speed)
					if reportedSpeed > 0 {
						diffRatio := math.Abs(computedSpeed-reportedSpeed) / reportedSpeed
						VehicleSpeedDiscrepancyRatioGauge.WithLabelValues(vehicleID, agencyID, strconv.Itoa(serverID)).Set(diffRatio)
					}
				}
			}
		}

		// Save last seen data
		vehicleLastSeen.Set(serverID, vehicleID, LastSeen{
			Time: seenAt,
			Lat:  lat,
			Lon:  lon,
		})
	}

	TrackedVehiclesGauge.WithLabelValues(strconv.Itoa(serverID)).Set(float64(vehicleLastSeen.Count(serverID)))

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
