package gtfs

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// DownloadGTFSBundles downloads GTFS bundles for each server and caches them locally.
func DownloadGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger, store *geo.BoundingBoxStore) {
	for _, server := range servers {
		hash := sha1.Sum([]byte(server.GtfsUrl))
		hashStr := hex.EncodeToString(hash[:])
		cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", server.ID, hashStr))

		_, err := DownloadGTFSBundle(server.GtfsUrl, cacheDir, server.ID, hashStr)
		if err != nil {
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("server_id", fmt.Sprintf("%d", server.ID)),
				ExtraContext: map[string]interface{}{
					"gtfs_url": server.GtfsUrl,
				},
				Level: sentry.LevelError,
			})
			logger.Error("Failed to download GTFS bundle", "server_id", server.ID, "error", err)
			continue
		}
		logger.Info("Successfully downloaded GTFS bundle", "server_id", server.ID, "path", cachePath)

		staticData, err := ParseGTFSFromCache(cachePath, server.ID)
		if err != nil {
			logger.Error("Failed to parse GTFS bundle", "server_id", server.ID, "error", err)
			continue
		}

		// compute bounding box for each downloaded GTFS bundle
		bbox, err := geo.ComputeBoundingBox(staticData.Stops)
		if err != nil {
			logger.Warn("Could not compute bounding box", "server_id", server.ID, "error", err)
			continue
		}

		// one bounding box per server
		store.Set(server.ID, bbox)
		logger.Info("Computed bounding box", "server_id", server.ID, "bbox", bbox)
	}
}

// RefreshGTFSBundles periodically downloads GTFS bundles at the specified interval.
func RefreshGTFSBundles(servers []models.ObaServer, cacheDir string, logger *slog.Logger, interval time.Duration, store *geo.BoundingBoxStore) {
	for {
		time.Sleep(interval)
		DownloadGTFSBundles(servers, cacheDir, logger, store)
	}
}

func DownloadGTFSBundle(url string, cacheDir string, serverID int, hashStr string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		sentry.CaptureException(err)
		return "", err
	}
	defer resp.Body.Close()

	cacheFileName := fmt.Sprintf("server_%d_%s.zip", serverID, hashStr)
	cachePath := filepath.Join(cacheDir, cacheFileName)

	out, err := os.Create(cachePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	return cachePath, nil
}

// ParseGTFSFromCache reads a GTFS bundle from the cache and parses it into a gtfs.Static object.
// It returns the parsed static data or an error if parsing fails.
func ParseGTFSFromCache(cachePath string, serverID int) (*gtfs.Static, error) {
	fileBytes, err := os.ReadFile(cachePath)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}

	staticData, err := gtfs.ParseStatic(fileBytes, gtfs.ParseStaticOptions{})
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"cache_path": cachePath,
			},
		})
		return nil, err
	}

	return staticData, nil
}

// GetStopLocationsByIDs retrieves stop locations by their IDs from the GTFS cache.
// It returns a map of stop IDs to gtfs.Stop objects.

func GetStopLocationsByIDs(cachePath string, serverID int, stopIDs []string) (map[string]gtfs.Stop, error) {
	staticData, err := ParseGTFSFromCache(cachePath, serverID)
	if err != nil {
		return nil, err
	}

	stopIDSet := make(map[string]struct{}, len(stopIDs))
	for _, id := range stopIDs {
		stopIDSet[id] = struct{}{}
	}

	result := make(map[string]gtfs.Stop)
	for _, stop := range staticData.Stops {
		if _, exists := stopIDSet[stop.Id]; exists {
			result[stop.Id] = stop
		}
	}
	return result, nil
}

// FetchGTFSRTFeed fetches the GTFS-RT feed from the specified server.
// It returns the HTTP response or an error if the request fails.
func FetchGTFSRTFeed(server models.ObaServer) (*http.Response, error) {
	parsedURL, err := url.Parse(server.VehiclePositionUrl)
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS-RT URL: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return nil, err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		report.ReportError(err)
		return nil, err
	}
	if server.GtfsRtApiKey != "" && server.GtfsRtApiValue != "" {
		req.Header.Set(server.GtfsRtApiKey, server.GtfsRtApiValue)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("failed to fetch GTFS-RT feed: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return nil, err
	}

	return resp, nil
}

// GetEarliestAndLatestServiceDates returns the earliest and latest service end dates
// from the GTFS static data's calendar entries.
//
// This is used as a workaround because the GTFS library does not currently support
// parsing `feed_info.txt`, which usually provides feed start/end dates.
//
// Instead, this function infers expiration information by scanning all `calendar.txt`
// entries (i.e., service periods), and returns the minimum and maximum `EndDate` values.
//
// Returns an error if no services are found in the bundle.
func GetEarliestAndLatestServiceDates(staticData *gtfs.Static) (earliestEndDate, latestEndDate time.Time, err error) {
	if len(staticData.Services) == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("no services found in GTFS bundle")
	}
	earliestEndDate = staticData.Services[0].EndDate
	latestEndDate = staticData.Services[0].EndDate
	for _, service := range staticData.Services {
		if service.EndDate.Before(earliestEndDate) {
			earliestEndDate = service.EndDate
		}
		if service.EndDate.After(latestEndDate) {
			latestEndDate = service.EndDate
		}
	}
	return earliestEndDate, latestEndDate, nil
}
