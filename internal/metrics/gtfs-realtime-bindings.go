package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func CountVehiclePositions(server models.ObaServer) (int, error) {
	parsedURL, err := url.Parse(server.VehiclePositionUrl)
	if err != nil {
		return 0, fmt.Errorf("failed to parse GTFS-RT URL: %v", err)
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create HTTP request: %v", err)
	}
	if server.GtfsRtApiKey != "" && server.GtfsRtApiValue != "" {
		req.Header.Set(server.GtfsRtApiKey, server.GtfsRtApiValue)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		sentry.CaptureException(err)
		return 0, fmt.Errorf("failed to fetch GTFS-RT feed: %v", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read GTFS-RT feed: %v", err)
	}

	realtimeData, err := gtfs.ParseRealtime(data, &gtfs.ParseRealtimeOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse GTFS-RT feed: %v", err)
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
		sentry.CaptureException(err)
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
		return fmt.Errorf("failed to count vehicle positions from GTFS-RT: %v", err)
	}

	apiVehicleCount, err := VehiclesForAgencyAPI(server)
	if err != nil {
		return fmt.Errorf("failed to count vehicle positions from API: %v", err)
	}

	match := 0
	if gtfsRtVehicleCount == apiVehicleCount {
		match = 1
	}

	VehicleCountMatch.WithLabelValues(server.AgencyID, strconv.Itoa(server.ID)).Set(float64(match))

	return nil
}

func isValidLatLong(lat, long float64) bool {
	return lat >= -90 && lat <= 90 && long >= -180 && long <= 180
}

func CountVehiclePosition(server models.ObaServer) (int, error) {
	parsedURL, err := url.Parse(server.VehiclePositionUrl)
	if err != nil {
		return 0, fmt.Errorf("failed to parse GTFS-RT URL: %v", err)
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create HTTP request: %v", err)
	}
	if server.GtfsRtApiKey != "" && server.GtfsRtApiValue != "" {
		req.Header.Set(server.GtfsRtApiKey, server.GtfsRtApiValue)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		sentry.CaptureException(err)
		return 0, fmt.Errorf("failed to fetch GTFS-RT feed: %v", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read GTFS-RT feed: %v", err)
	}

	realTimeData, err := gtfs.ParseRealtime(data, &gtfs.ParseRealtimeOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse GTFS-RT feed: %v", err)
	}

	totalCount := len(realTimeData.Vehicles)
	invalidCount := 0

	//Count invalid postions
	for _, vehicle := range realTimeData.Vehicles {
		fmt.Println(float64(*vehicle.Position.Latitude), float64(*vehicle.Position.Longitude))
		if !isValidLatLong(float64(*vehicle.Position.Latitude), float64(*vehicle.Position.Longitude)) {
			invalidCount++
		}
	}

	// Update metrics
	RealtimeVehiclePositions.WithLabelValues(
		server.VehiclePositionUrl,
		strconv.Itoa(server.ID),
	).Set(float64(totalCount))

	InvalidVehiclePositions.WithLabelValues(
		server.VehiclePositionUrl,
		strconv.Itoa(server.ID),
	).Set(float64(invalidCount))

	if totalCount > 0 {
		validPercent := float64(totalCount-invalidCount) / float64(totalCount) * 100
		ValidVehiclePositionsPercent.WithLabelValues(
			server.VehiclePositionUrl,
			strconv.Itoa(server.ID),
		).Set(validPercent)
	}

	return totalCount, nil

}
