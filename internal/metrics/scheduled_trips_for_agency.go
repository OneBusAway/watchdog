package metrics

import (
	"context"

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

	return len(response.Data.Entry.Trips), nil
}
