package metrics

import (
	"context"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
)

func AllScheduledTripsForRoute(server models.ObaServer, routeId string) (int, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.ScheduleForRoute.Get(ctx, routeId, onebusaway.ScheduleForRouteGetParams{})

	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	ScheduledTripsPerRoute.WithLabelValues(
		strconv.Itoa(server.ID),
		routeId,
	).Set(float64(len(response.Data.Entry.Trips)))

	return len(response.Data.Entry.Trips), nil
}

func AllScheduledTripsForAgency(server models.ObaServer, agencyId string) (int, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.RoutesForAgency.List(ctx, agencyId)

	if err != nil {
		sentry.CaptureException(err)
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	totalTrips := 0
	for _, route := range response.Data.List {
		trips, err := AllScheduledTripsForAgency(server, route.ID)

		if err != nil {
			sentry.CaptureException(err)
			return 0, err
		}

		totalTrips += trips
	}

	ScheduledTripsPerAgency.WithLabelValues(
		strconv.Itoa(server.ID),
		agencyId,
	).Set(float64(totalTrips))

	return totalTrips, nil

}
