package metrics

import (
	"context"
	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"strconv"
	"watchdog.onebusaway.org/internal/models"
)

func GetScheduledRoutesForStop(server models.ObaServer, stopID string) ([]string, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.ScheduleForStop.Get(ctx, stopID, onebusaway.ScheduleForStopGetParams{})

	if err != nil {
		sentry.CaptureException(err)
		return nil, err
	}

	if response == nil {
		return nil, nil
	}

	ScheduleRouteForStop.WithLabelValues(
		strconv.Itoa(server.ID),
		stopID,
	).Set(float64(len(response.Data.Entry.StopRouteSchedules)))

	routeIDs := make([]string, 0, len(response.Data.Entry.StopRouteSchedules))

	for _, schedule := range response.Data.Entry.StopRouteSchedules {
		routeIDs = append(routeIDs, schedule.RouteID)
	}
	return routeIDs, nil
}

func getLocationForStop(server models.ObaServer, stopID string) ([]float64, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.Stop.Get(ctx, stopID)

	if err != nil {
		sentry.CaptureException(err)
		return nil, err
	}

	if response == nil {
		return nil, nil
	}

	location := []float64{response.Data.Entry.Lat, response.Data.Entry.Lon}
	return location, nil
}
