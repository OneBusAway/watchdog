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
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func CountVehiclePositions(server models.ObaServer, reporter *report.Reporter) (int, error) {
	parsedURL, err := url.Parse(server.VehiclePositionUrl)
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS-RT URL: %v", err)
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return 0, err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		err = fmt.Errorf("failed to create HTTP request: %v", err)
		reporter.ReportError(err)
		return 0, err
	}
	if server.GtfsRtApiKey != "" && server.GtfsRtApiValue != "" {
		req.Header.Set(server.GtfsRtApiKey, server.GtfsRtApiValue)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("failed to fetch GTFS-RT feed: %v", err)
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("failed to read GTFS-RT feed: %v", err)
		reporter.ReportError(err)
		return 0, err
	}

	realtimeData, err := gtfs.ParseRealtime(data, &gtfs.ParseRealtimeOptions{})
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS-RT feed: %v", err)
		reporter.ReportError(err)
		return 0, err
	}

	count := len(realtimeData.Vehicles)

	RealtimeVehiclePositions.WithLabelValues(
		server.VehiclePositionUrl,
		strconv.Itoa(server.ID),
	).Set(float64(count))

	return count, nil
}

func VehiclesForAgencyAPI(server models.ObaServer, reporter *report.Reporter) (int, error) {

	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.VehiclesForAgency.List(ctx, server.AgencyID, onebusaway.VehiclesForAgencyListParams{})

	if err != nil {
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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

func CheckVehicleCountMatch(server models.ObaServer, reporter *report.Reporter) error {
	gtfsRtVehicleCount, err := CountVehiclePositions(server, reporter)
	if err != nil {
		err := fmt.Errorf("failed to count vehicle positions from GTFS-RT: %v", err)
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
		})
		return err
	}

	apiVehicleCount, err := VehiclesForAgencyAPI(server, reporter)
	if err != nil {
		err := fmt.Errorf("failed to count vehicle positions from API: %v", err)
		reporter.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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
