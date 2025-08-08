package metrics

import (
	"fmt"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// CheckBundleExpiration calculates the number of days remaining until the earliest and latest
// service end dates in the GTFS static bundle associated with a given server.
//
// It retrieves the static GTFS data from the provided StaticStore using the server ID,
// and then computes the number of days remaining until both the earliest and latest service
// end dates based on the provided current time.
//
// Parameters:
//   - staticStore: a pointer to StaticStore that holds GTFS data for multiple servers.
//   - currentTime: the current time used to calculate the expiration durations (converted to UTC).
//   - server: the ObaServer whose bundle expiration should be checked.
//
// Returns:
//   - int: days until the earliest service end date.
//   - int: days until the latest service end date.
//   - error: any error encountered during processing.
func CheckBundleExpiration(staticStore *gtfs.StaticStore, currentTime time.Time, server models.ObaServer) (int, int, error) {
	currentTime = currentTime.UTC()
	staticData, ok := staticStore.Get(server.ID)
	if !ok {
		err := fmt.Errorf("there is no bundle for server %v", server.ID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			Level: sentry.LevelWarning,
		})
		return 0, 0, err
	}
	if staticData == nil {
		err := fmt.Errorf("static data is nil for server %v", server.ID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			Level: sentry.LevelWarning,
		})
		return 0, 0, err
	}
	earliestEndDate, latestEndDate, err := gtfs.GetEarliestAndLatestServiceDates(staticData)

	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  utils.MakeMap("server_id", strconv.Itoa(server.ID)),
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
