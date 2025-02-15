package metrics

import (
	"context"
	"fmt"
	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"log"
	"time"
	"watchdog.onebusaway.org/internal/models"
)

type TripDetail struct {
	tripID string
	stopID string
}

func CheckArrivalsAccuracy(server models.ObaServer) (float64, float64, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	tripDetails, err := getStops(server)
	if err != nil {
		sentry.CaptureException(err)
		return 0, 0, err
	}

	totalArrivals := make(map[string]int)
	predictedArrivals := make(map[string]int)
	perfectPredictions := make(map[string]int)

	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix() * 1000

	for _, detail := range tripDetails {
		params := onebusaway.ArrivalAndDepartureGetParams{
			ServiceDate: onebusaway.F(midnight),
			TripID:      onebusaway.F(detail.tripID),
		}

		response, err := client.ArrivalAndDeparture.Get(ctx, detail.stopID, params, option.WithQueryAdd("key", "Test"))
		if err != nil || response == nil {
			sentry.CaptureException(err)
			continue
		}

		scheduledArrivalTime := response.Data.Entry.ScheduledArrivalTime
		predictedArrivalTime := response.Data.Entry.PredictedArrivalTime

		totalArrivals[detail.stopID]++

		if predictedArrivalTime > 0 {
			predictedArrivals[detail.stopID]++
		}

		if scheduledArrivalTime == predictedArrivalTime {
			perfectPredictions[detail.stopID]++
		}
	}

	var totalPredicted, totalPerfect, totalStops int
	for stopID := range totalArrivals {
		totalPredicted += predictedArrivals[stopID]
		totalPerfect += perfectPredictions[stopID]
		totalStops += totalArrivals[stopID]

		predictedRatio := float64(predictedArrivals[stopID]) / float64(totalArrivals[stopID])
		perfectPredictionRate := float64(perfectPredictions[stopID]) / float64(totalArrivals[stopID])

		PredictArrivalRate.WithLabelValues(server.AgencyID, stopID).Set(predictedRatio)
		PerfectPredictionRate.WithLabelValues(server.AgencyID, stopID).Set(perfectPredictionRate)
		// **Check and Trigger Alerts per Stop**
		checkStopAlerts(server.AgencyID, stopID, predictedRatio, perfectPredictionRate)
	}

	agencyPredictedRatio := float64(totalPredicted) / float64(totalStops)
	agencyPerfectPredictionRate := float64(totalPerfect) / float64(totalStops)

	PredictArrivalRate.WithLabelValues(server.AgencyID, "all").Set(agencyPredictedRatio)
	PerfectPredictionRate.WithLabelValues(server.AgencyID, "all").Set(agencyPerfectPredictionRate)
	checkStopAlerts(server.AgencyID, "all", agencyPredictedRatio, agencyPerfectPredictionRate)

	return agencyPredictedRatio, agencyPerfectPredictionRate, nil
}

// get stops for agency
func getStops(server models.ObaServer) ([]TripDetail, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	// get route for agency
	response, err := client.RoutesForAgency.List(ctx, server.AgencyID, option.WithQueryAdd("key", "Test"))
	if err != nil || response == nil {
		return nil, err
	}
	var routeIDs []string
	for _, route := range response.Data.List {
		routeIDs = append(routeIDs, route.ID)
	}

	// get trips for route
	var tripIDs []string
	for _, routeID := range routeIDs {
		response, err := client.TripsForRoute.List(ctx, routeID, onebusaway.TripsForRouteListParams{}, option.WithQueryAdd("key", "Test"))
		if err != nil || response == nil {
			sentry.CaptureException(err)
			continue
		}
		for _, trip := range response.Data.List {
			tripIDs = append(tripIDs, trip.TripID)
		}
	}

	// get stops for trip
	var tripDetails []TripDetail
	for _, tripID := range tripIDs {
		response, err := client.TripDetails.Get(ctx, tripID, onebusaway.TripDetailGetParams{}, option.WithQueryAdd("key", "Test"))
		if err != nil || response == nil {
			sentry.CaptureException(err)
			continue
		}

		for _, stop := range response.Data.Entry.Schedule.StopTimes {
			tripDetails = append(tripDetails, TripDetail{tripID: tripID, stopID: stop.StopID})
		}

	}

	return tripDetails, nil
}

func checkStopAlerts(agencyID, stopID string, predictedRatio, perfectPredictionRate float64) {
	if predictedRatio < 0.5 {
		alertMsg := fmt.Sprintf("[ALERT] Stop %s has LOW real-time data availability: %.4f%% predicted (Agency: %s)", stopID, predictedRatio*100, agencyID)
		log.Println(alertMsg)
		sentry.CaptureMessage(alertMsg)
		AlertLowDataAvailability.WithLabelValues(agencyID, stopID).Set(predictedRatio) // Set alert metric to 1
	}

	if perfectPredictionRate < 0.1 {
		alertMsg := fmt.Sprintf("[ALERT] Stop %s has POOR prediction accuracy: %.4f%% perfect (Agency: %s)", stopID, perfectPredictionRate*100, agencyID)
		log.Println(alertMsg)
		sentry.CaptureMessage(alertMsg)
		AlertPoorPredictionAccuracy.WithLabelValues(agencyID, stopID).Set(perfectPredictionRate) // Set alert metric to 1
	}
}
