package metrics

import (
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// CheckBundleExpiration calculates the number of days remaining until the GTFS bundle expires.
func CheckBundleExpiration(cachePath string, currentTime time.Time, server models.ObaServer) (int, int, error) {
	staticData, err := gtfs.ParseGTFSFromCache(cachePath, server.ID)
	if err != nil {
		return 0, 0, err
	}

	earliestEndDate, latestEndDate, err := gtfs.GetEarliestAndLatestServiceDates(staticData)

	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
			Level: sentry.LevelWarning,
		})
		return 0, 0, err
	}

	daysUntilEarliestExpiration := int(earliestEndDate.Sub(currentTime).Hours() / 24)
	daysUntilLatestExpiration := int(latestEndDate.Sub(currentTime).Hours() / 24)

	BundleEarliestExpirationGauge.WithLabelValues(strconv.Itoa(server.ID)).Set(float64(daysUntilEarliestExpiration))
	BundleLatestExpirationGauge.WithLabelValues(strconv.Itoa(server.ID)).Set(float64(daysUntilLatestExpiration))

	return daysUntilEarliestExpiration, daysUntilLatestExpiration, nil
}
