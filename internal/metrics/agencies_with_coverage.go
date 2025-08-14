package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// checkAgenciesWithCoverage retrieves the number of agencies in the GTFS static bundle
// associated with the given server. It reports the count to the AgenciesInStaticGtfs Prometheus metric.
//
// Returns the agency count if the bundle is present and valid.
// Returns an error if the bundle is missing, nil, or contains no agencies.
func checkAgenciesWithCoverage(staticStore *gtfs.StaticStore, server models.ObaServer) (int, error) {
	staticData, ok := staticStore.Get(server.ID)
	if !ok {
		err := fmt.Errorf("there is no bundle for server %v", server.ID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			Level: sentry.LevelWarning,
		})
		return 0, err
	}
	if staticData == nil {
		err := fmt.Errorf("static data is nil for server %v", server.ID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			Level: sentry.LevelWarning,
		})
		return 0, err
	}
	if len(staticData.Agencies) == 0 {
		err := fmt.Errorf("no agencies found in GTFS bundle for server %v", server.ID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": strconv.Itoa(server.ID),
			},
			Level: sentry.LevelWarning,
		})
		return 0, err
	}

	AgenciesInStaticGtfs.WithLabelValues(
		strconv.Itoa(server.ID),
	).Set(float64(len(staticData.Agencies)))

	return len(staticData.Agencies), nil
}

// getAgenciesWithCoverage calls the OBA `agencies-with-coverage` API endpoint
// for the given server and returns the number of agencies in the real-time feed.
// The count is also reported to the AgenciesWithCoverage Prometheus metric.
//
// This function is used to collect live data for comparison against the GTFS static bundle.
//
// Returns the number of real-time agencies on success.
// Returns an error if the API call fails or the response is invalid.
func getAgenciesWithCoverage(server models.ObaServer) (int, error) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()

	response, err := client.AgenciesWithCoverage.List(ctx)

	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"server_id":    strconv.Itoa(server.ID),
				"oba_base_url": server.ObaBaseURL,
			},
		})
		return 0, err
	}

	if response == nil {
		return 0, nil
	}

	AgenciesInCoverageEndpoint.WithLabelValues(
		strconv.Itoa(server.ID),
	).Set(float64(len(response.Data.List)))

	return len(response.Data.List), nil
}

// checkAgenciesWithCoverageMatch compares the number of agencies in the GTFS static bundle
// with the number of agencies returned by the real-time `agencies-with-coverage` API for the given server.
// It sets the AgenciesCoverageMatch Prometheus metric to 1 if the counts match, or 0 if they differ.
//
// Returns an error if reading the static bundle or calling the API fails.
func checkAgenciesWithCoverageMatch(staticStore *gtfs.StaticStore, logger *slog.Logger, server models.ObaServer) error {
	staticGtfsAgenciesCount, err := checkAgenciesWithCoverage(staticStore, server)
	if err != nil {
		return err
	}

	coverageAgenciesCount, err := getAgenciesWithCoverage(server)

	if err != nil {
		return fmt.Errorf("error getting remote agencies with coverage data: %w", err)
	}

	matchValue := 0
	if coverageAgenciesCount == staticGtfsAgenciesCount {
		matchValue = 1
	}

	AgenciesMatch.WithLabelValues(strconv.Itoa(server.ID)).Set(float64(matchValue))

	return nil
}
