package metrics

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// CheckBundleExpiration calculates the number of days remaining until the GTFS bundle expires.
func CheckBundleExpiration(cachePath string, logger *slog.Logger, currentTime time.Time, server models.ObaServer) (int, int, error) {

	file, err := os.Open(cachePath)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	// Convert the file into a byte slice
	fileInfo, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	fileBytes := make([]byte, fileInfo.Size())
	_, err = file.Read(fileBytes)
	if err != nil {
		return 0, 0, err
	}

	staticData, err := gtfs.ParseStatic(fileBytes, gtfs.ParseStaticOptions{})
	if err != nil {
		return 0, 0, err
	}

	if len(staticData.Services) == 0 {
		return 0, 0, fmt.Errorf("no services found in GTFS bundle")
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
