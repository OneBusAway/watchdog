package metrics

import (
	"context"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
)

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

	if response == nil || response.Data.Entry.Trips == nil {
		return 0, nil
	}

	ScheduleTripForRoute.WithLabelValues(
		strconv.Itoa(server.ID),
		routeID,
	).Set(float64(len(response.Data.Entry.Trips)))

	return len(response.Data.Entry.Trips), nil
}
