package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// RegionBoundary represents the geographical boundaries of a transit region
type RegionBoundary struct {
	MinLat float64
	MaxLat float64
	MinLon float64
	MaxLon float64
}

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

// ExtractRegionBoundaries extracts the geographical boundaries from GTFS shapes data
func ExtractRegionBoundaries(gtfsZipPath string) (*RegionBoundary, error) {
	// Read the GTFS static data from the zip file
	data, err := os.ReadFile(gtfsZipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read GTFS file: %v", err)
	}

	// Parse the GTFS static data
	staticData, err := gtfs.ParseStatic(data, gtfs.ParseStaticOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse GTFS static data: %v", err)
	}

	// Initialize boundary with extreme values in the opposite direction
	boundary := &RegionBoundary{
		MinLat: 90.0,
		MaxLat: -90.0,
		MinLon: 180.0,
		MaxLon: -180.0,
	}

	// Check if we have shapes data
	if len(staticData.Shapes) == 0 {
		return nil, fmt.Errorf("no shapes data found in GTFS feed")
	}

	// Process all shapes to find the region boundaries
	for _, shape := range staticData.Shapes {
		for _, point := range shape.Points {
			lat := float64(point.Latitude)
			lon := float64(point.Longitude)

			// Update boundaries
			if lat < boundary.MinLat {
				boundary.MinLat = lat
			}
			if lat > boundary.MaxLat {
				boundary.MaxLat = lat
			}
			if lon < boundary.MinLon {
				boundary.MinLon = lon
			}
			if lon > boundary.MaxLon {
				boundary.MaxLon = lon
			}
		}
	}

	// Add a small buffer to the boundaries (approximately 1 km)
	// This accounts for vehicles that might be slightly outside the exact shape paths
	// Might need to reconsider this
	const bufferDegrees = 0.01 // Roughly 1km at the equator
	boundary.MinLat -= bufferDegrees
	boundary.MaxLat += bufferDegrees
	boundary.MinLon -= bufferDegrees
	boundary.MaxLon += bufferDegrees

	return boundary, nil
}

// IsPositionWithinBoundary checks if a lat/lon position is within the given region boundary
func IsPositionWithinBoundary(lat, lon float64, boundary *RegionBoundary) bool {
	// First check if the position is a valid lat/long (basic sanity check)
	if !isValidLatLong(lat, lon) {
		return false
	}

	// Then check if it's within the region boundary
	return lat >= boundary.MinLat &&
		lat <= boundary.MaxLat &&
		lon >= boundary.MinLon &&
		lon <= boundary.MaxLon
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

	// Download and parse GTFS static data to extract region boundaries
	gtfsPath, err := utils.DownloadGTFSBundle(server.GtfsUrl, "cache", server.ID, "region_boundary")
	if err != nil {
		return 0, fmt.Errorf("failed to download GTFS bundle: %v", err)
	}

	boundary, err := ExtractRegionBoundaries(gtfsPath)
	if err != nil {
		return 0, fmt.Errorf("failed to extract region boundaries: %v", err)
	}

	totalCount := len(realTimeData.Vehicles)
	invalidCount := 0

	// Count invalid positions based on region boundaries
	for _, vehicle := range realTimeData.Vehicles {
		lat := float64(*vehicle.Position.Latitude)
		lon := float64(*vehicle.Position.Longitude)

		if !IsPositionWithinBoundary(lat, lon, boundary) {
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
