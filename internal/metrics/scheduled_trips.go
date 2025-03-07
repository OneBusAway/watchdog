package metrics

import (
	"context"
	"fmt"
	"os"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

func CountScheduledTripRoute(dataPath string, server models.ObaServer, routeID string) (int, error) {
	file, err := os.Open(dataPath)
	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}

	fileBytes := make([]byte, fileInfo.Size())
	_, err = file.Read(fileBytes)
	if err != nil {
		return 0, err
	}

	staticData, err := gtfs.ParseStatic(fileBytes, gtfs.ParseStaticOptions{})
	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}

	count := 0

	for _, trip := range staticData.Trips {
		if trip.Route.Id == routeID {
			count++
		}
	}

	return count, nil
}

func GetScheduledTripRoute(server models.ObaServer, routeID string) (int, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.ScheduleForRoute.Get(ctx, routeID, onebusaway.ScheduleForRouteGetParams{})

	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	ScheduleTripForRoute.WithLabelValues(
		strconv.Itoa(server.ID),
		routeID,
	).Set(float64(len(response.Data.Entry.Trips)))

	return len(response.Data.Entry.Trips), nil
}

func CheckScheduledTripRoute(dataPath string, server models.ObaServer, routeID string) error {
	apiTripRoutes, err := GetScheduledTripRoute(server, routeID)

	if err != nil {
		return fmt.Errorf("failed to count scheduled trip routes from API: %v", err)
	}

	gtfsrtTripRoutes, err := CountScheduledTripRoute(dataPath, server, routeID)
	if err != nil {
		return fmt.Errorf("failed to count scheduled trip routes from GTFS-RT: %v", err)
	}

	match := 0
	if apiTripRoutes == gtfsrtTripRoutes {
		match = 1
	}

	ScheduleTripForRoute.WithLabelValues(strconv.Itoa(server.ID), routeID).Set(float64(match))

	return nil
}
