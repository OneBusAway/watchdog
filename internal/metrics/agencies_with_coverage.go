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
)

func CheckAgenciesWithCoverage(cachePath string, server models.ObaServer) (int, error) {
	staticData, err := gtfs.ParseGTFSFromCache(cachePath, server.ID)
	if err != nil {
		return 0, fmt.Errorf("error parsing GTFS from cache: %w", err)
	}

	if len(staticData.Agencies) == 0 {
		report.ReportErrorWithSentryOptions(fmt.Errorf("no agencies found in GTFS bundle"), report.SentryReportOptions{
			Tags: map[string]string{
				"server_id": strconv.Itoa(server.ID),
			},
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
			Level: sentry.LevelWarning,
		})
		return 0, fmt.Errorf("no agencies found in GTFS bundle")
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

func CheckAgenciesWithCoverageMatch(cachePath string, logger *slog.Logger, server models.ObaServer) error {
	staticGtfsAgenciesCount, err := CheckAgenciesWithCoverage(cachePath, server)
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
