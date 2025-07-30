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

func CheckAgenciesWithCoverage(staticStore *gtfs.StaticStore, server models.ObaServer) (int, error) {
	staticData, ok := staticStore.Get(server.ID)
	if !ok {
		err := fmt.Errorf("there is no bundle for sever %v", server.ID)
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

func GetAgenciesWithCoverage(server models.ObaServer) (int, error) {
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

func CheckAgenciesWithCoverageMatch(staticStore *gtfs.StaticStore, logger *slog.Logger, server models.ObaServer) error {
	staticGtfsAgenciesCount, err := CheckAgenciesWithCoverage(staticStore, server)
	if err != nil {
		return err
	}

	coverageAgenciesCount, err := GetAgenciesWithCoverage(server)

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
