package gtfs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	remoteGtfs "github.com/jamespfennell/gtfs"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// downloadGTFSBundles fetches and processes GTFS static bundles for a list of OBA servers.
//
// For each server, it:
//   1. Downloads and parses the GTFS static bundle using the server's GTFS URL.
//   2. Stores the parsed data in the provided StaticStore.
//   3. Computes a geographic bounding box based on the stop locations in the static data.
//   4. Stores the bounding box in the provided BoundingBoxStore.
//
// Parameters:
//   - servers: A list of OBA servers, each containing a GTFS URL and unique ID.
//   - logger: A structured logger used to record success/failure logs for each server.
//   - boundingBoxStore: A store for bounding boxes, one per server.
//   - staticStore: A store for parsed GTFS static data, keyed by server ID.
//
// This function does not return an error; failures are handled and reported per-server.

func downloadGTFSBundles(ctx context.Context, servers []models.ObaServer, logger *slog.Logger, boundingBoxStore *geo.BoundingBoxStore, staticStore *StaticStore, maxRetries int) {
	var wg sync.WaitGroup
	for _, server := range servers {
		s := server
		wg.Add(1)
		go func() {
			defer wg.Done()

			staticBundle, err := downloadGTFSBundle(ctx, s.GtfsUrl, s.ID, maxRetries)
			if err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: utils.MakeMap("server_id", fmt.Sprintf("%d", server.ID)),
					ExtraContext: map[string]interface{}{
						"gtfs_url": s.GtfsUrl,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to download GTFS bundle", "server_id", s.ID, "error", err)
				return
			}
			logger.Info("Successfully downloaded GTFS bundle", "server_id", s.ID)

			err = storeGTFSBundle(staticBundle, s.ID, staticStore, boundingBoxStore)
			if err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: utils.MakeMap("server_id", fmt.Sprintf("%d", s.ID)),
					ExtraContext: map[string]interface{}{
						"gtfs_url": s.GtfsUrl,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to store GTFS bundle", "server_id", s.ID, "error", err)
			}
		}()
	}
	wg.Wait()
}

// refreshGTFSBundles periodically refreshes GTFS static bundles for a list of OBA servers.
//
// It runs in a loop, triggered at the specified interval, and performs the following:
//  1. Logs the refresh operation.
//  2. Calls DownloadGTFSBundles to fetch, parse, and store updated GTFS data.
//  3. Updates geographic bounding boxes based on the downloaded data.
//
// The function listens for context cancellation (`ctx.Done()`) to gracefully stop the refresh routine.
//
// Parameters:
//   - ctx: Context used to cancel the refresh routine gracefully.
//   - servers: List of OBA servers to fetch GTFS data from.
//   - logger: Logger for structured logging of refresh activity.
//   - interval: Time duration between each refresh cycle.
//   - boundingBoxstore: Store to keep geographic bounding boxes per server.
//   - staticStore: Store to keep parsed GTFS static data per server.
func refreshGTFSBundles(ctx context.Context, servers []models.ObaServer, logger *slog.Logger, interval time.Duration, boundingBoxstore *geo.BoundingBoxStore, staticStore *StaticStore, maxRetries int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping GTFS bundle refresh routine")
			return
		case <-ticker.C:
			logger.Info("Refreshing GTFS bundles")
			downloadGTFSBundles(ctx, servers, logger, boundingBoxstore, staticStore, maxRetries)
		}
	}
}

// downloadAndStoreGTFSBundle fetches a GTFS static bundle from the provided URL,
// parses it, and stores the resulting static data in the given StaticStore using the serverID as the key.
//
// It performs the following steps:
//   1. Makes an HTTP GET request to download the GTFS bundle.
//   2. Reads and parses the response body as GTFS static data.
//   3. Stores the parsed data in the StaticStore.
//
// Parameters:
//   - url: The URL of the GTFS static bundle (usually a zip file).
//   - serverID: The identifier used to store and retrieve the static data from the store.
//   - staticStore: The in-memory store that holds GTFS static data indexed by server ID.
//
// Returns:
//   - gtfs static data
//   - boolean severs as a sign if the request reached the server or not (server timeout or down)
//   - error: Describes what went wrong, or nil if the operation was successful.

func downloadGTFSBundle(ctx context.Context, url string, serverID int, maxRetries int) (*remoteGtfs.Static, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = fmt.Errorf("failed to create request for %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return nil, err
	}

	resp, err := config.DoWithBackoff(ctx, client, req, maxRetries)

	if err != nil {
		err = fmt.Errorf("failed to make GET request to %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("unexpected response status %d when downloading GTFS bundle from %s", resp.StatusCode, url)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"url":    url,
				"status": resp.Status,
			},
		})
		return nil, err
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("failed to read GTFS bundle response body from %s: %w", url, err)
		report.ReportError(err)
		return nil, err
	}

	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS static data from %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return nil, err
	}
	return staticBundle, nil

}

func storeGTFSBundle(staticBundle *remoteGtfs.Static, serverID int, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore) error {
	// StaticData is a wrapper around the GTFS static bundle
	// that includes only the parts we use in the application.
	// So we do not keep the whole GTFS static bundle in memory,
	// but only the parts we need.
	staticData := models.NewStaticData(staticBundle)
	staticBundle = nil // drop reference, GC can collect earlier
	staticStore.Set(serverID, staticData)
	// compute bounding box for each downloaded GTFS bundle
	bbox, err := geo.ComputeBoundingBox(staticData.Stops)
	if err != nil {
		return fmt.Errorf("could not compute bounding box for server_id %d: %v", serverID, err)
	}
	// one bounding box per server
	boundingBoxStore.Set(serverID, bbox)
	return nil
}

// getStopLocationsByIDs retrieves stop locations by their IDs from the GTFS cache.
// It returns a map of stop IDs to gtfs.Stop objects.

func getStopLocationsByIDs(serverID int, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	staticData, ok := staticStore.Get(serverID)
	if !ok || staticData == nil {
		err := fmt.Errorf("no GTFS static data found for server ID %d", serverID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(serverID)),
		})
		return nil, err
	}

	stopIDSet := make(map[string]struct{}, len(stopIDs))
	for _, id := range stopIDs {
		stopIDSet[id] = struct{}{}
	}

	result := make(map[string]remoteGtfs.Stop)
	for _, stop := range staticData.Stops {
		if _, exists := stopIDSet[stop.Id]; exists {
			result[stop.Id] = stop
		}
	}
	return result, nil
}

// fetchAndStoreGTFSRTFeed fetches the GTFS-Realtime (GTFS-RT) vehicle position feed
// from the specified server, parses the response, and stores it safely in the
// provided RealtimeStore.
//
// The realtimeStore is designed to be thread-safe, and this function ensures
// that the parsed data is written using the store’s locking mechanisms,
// making it safe for concurrent access across goroutines.

func fetchAndStoreGTFSRTFeed(server models.ObaServer, realtimeStore *RealtimeStore, client *http.Client) error {
	parsedURL, err := url.Parse(server.VehiclePositionUrl)
	if err != nil {
		err = fmt.Errorf("failed to parse GTFS-RT URL: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return err
	}

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		report.ReportError(err)
		return err
	}

	if server.GtfsRtApiKey != "" && server.GtfsRtApiValue != "" {
		req.Header.Set(server.GtfsRtApiKey, server.GtfsRtApiValue)
	}

	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("failed to fetch GTFS-RT feed: %v", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": server.VehiclePositionUrl,
			},
		})
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		report.ReportError(err)
		return err
	}

	gtfsRT, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		report.ReportError(err)
		return err
	}
	realtimeData := models.NewRealtimeData(gtfsRT)
	gtfsRT = nil // drop reference, GC can collect earlier
	realtimeStore.Set(realtimeData)
	return nil
}

// getEarliestAndLatestServiceDates returns the earliest and latest service end dates
// from the GTFS static data's calendar entries.
//
// This is used as a workaround because the GTFS library does not currently support
// parsing `feed_info.txt`, which usually provides feed start/end dates.
//
// Instead, this function infers expiration information by scanning all `calendar.txt`
// entries (i.e., service periods), and returns the minimum and maximum `EndDate` values.
//
// Returns an error if no services are found in the bundle.
func getEarliestAndLatestServiceDates(staticData *models.StaticData) (earliestEndDate, latestEndDate time.Time, err error) {
	if staticData == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("static data is nil")
	}
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
