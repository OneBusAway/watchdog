package metrics

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// CheckBundleExpiration calculates the number of days remaining until the GTFS bundle expires.
func CheckBundleExpiration(cachePath string, logger *slog.Logger, currentTime time.Time, server models.ObaServer) (int, int, error) {
	staticData, err := gtfs.ParseGTFSFromCache(cachePath, server.ID)
	if err != nil {
		return 0, 0, err
	}

	if len(staticData.Services) == 0 {
		errMsg := fmt.Errorf("no services found in GTFS bundle")
		report.ReportErrorWithSentryOptions(errMsg, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
			Level: sentry.LevelWarning,
		})
		return 0, 0, errMsg
	}

	// Get the earliest and latest expiration dates
	// This is workaround because the GTFS library does not support feed_info.txt
	earliestEndDate := staticData.Services[0].EndDate
	latestEndDate := staticData.Services[0].EndDate
	for _, service := range staticData.Services {
		if service.EndDate.Before(earliestEndDate) {
			earliestEndDate = service.EndDate
		}
		if service.EndDate.After(latestEndDate) {
			latestEndDate = service.EndDate
		}
	}

	daysUntilEarliestExpiration := int(earliestEndDate.Sub(currentTime).Hours() / 24)
	daysUntilLatestExpiration := int(latestEndDate.Sub(currentTime).Hours() / 24)

	BundleEarliestExpirationGauge.WithLabelValues(strconv.Itoa(server.ID)).Set(float64(daysUntilEarliestExpiration))
	BundleLatestExpirationGauge.WithLabelValues(strconv.Itoa(server.ID)).Set(float64(daysUntilLatestExpiration))

	return daysUntilEarliestExpiration, daysUntilLatestExpiration, nil
}
