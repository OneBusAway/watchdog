package gtfs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// downloadGTFSBundles fetches and processes GTFS static bundles concurrently for a list of OBA servers.
//
// For each server, it starts a dedicated goroutine that:
//   1. Attempts to download and parse the GTFS static bundle from the server’s GTFS URL,
//      using exponential backoff with retries (up to maxRetries).
//   2. Stores the parsed GTFS static data in the provided StaticStore, keyed by server ID.
//   3. Computes a geographic bounding box from the stop locations in the static data.
//   4. Stores the bounding box in the provided BoundingBoxStore.
//
// Concurrency:
//   - A goroutine is launched for each server.
//   - sync.WaitGroup is used to ensure all goroutines complete before the function returns.
//   - Errors are handled per-server, reported via Sentry and logs, but do not stop processing other servers.
//
// Parameters:
//   - ctx: Context used to manage cancellation and timeouts across all goroutines.
//   - servers: A list of OBA servers, each containing a GTFS URL and unique ID.
//   - logger: A structured logger for recording success/failure logs.
//   - boundingBoxStore: A store for computed bounding boxes, one per server.
//   - staticStore: A store for parsed GTFS static data, keyed by server ID.
//   - maxRetries: The maximum number of retries (with exponential backoff) when downloading a bundle.
//
// This function does not return an error; failures are handled and reported individually per server.

func downloadGTFSBundles(ctx context.Context, servers []models.ObaServer, logger *slog.Logger, boundingBoxStore *geo.BoundingBoxStore, staticStore *StaticStore, maxRetries int) {
	var wg sync.WaitGroup
	for _, server := range servers {
		s := server
		wg.Add(1)
		go func() {
			defer wg.Done()
			bundles := make([]*remoteGtfs.Static, 0, len(s.GtfsURLs))
			for _, gtfsURL := range s.GtfsURLs {
				staticBundle, err := downloadGTFSBundle(ctx, gtfsURL, s.AgencyID, maxRetries)
				if err == nil {
					bundles = append(bundles, staticBundle)
					continue
				}
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: utils.MakeMap("agency_id", s.AgencyID),
					ExtraContext: map[string]interface{}{
						"gtfs_url": gtfsURL,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to download GTFS bundle", "agency_id", s.AgencyID, "error", err)
				return
			}
			if err := storeGTFSBundles(bundles, s.AgencyID, staticStore, boundingBoxStore); err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:  utils.MakeMap("agency_id", s.AgencyID),
					Level: sentry.LevelError,
				})
				logger.Error("Failed to store GTFS bundles", "agency_id", s.AgencyID, "error", err)
			}
		}()
	}
	wg.Wait()
}

// refreshGTFSBundles periodically refreshes GTFS static bundles for a list of OBA servers.
//
// It runs in a loop, triggered at the specified interval, and performs the following:
//   1. Logs the refresh operation.
//   2. Calls downloadGTFSBundles to fetch, parse, and store updated GTFS data for all servers.
//      - Each server’s bundle download uses exponential backoff with retries, up to maxRetries attempts.
//   3. Updates geographic bounding boxes based on the downloaded data.
//
// The function listens for context cancellation (`ctx.Done()`) to gracefully stop the refresh routine.
//
// Parameters:
//   - ctx: Context used to cancel the refresh routine gracefully.
//   - servers: List of OBA servers to fetch GTFS data from.
//   - logger: Logger for structured logging of refresh activity.
//   - interval: Time duration between each refresh cycle.
//   - boundingBoxStore: Store to keep geographic bounding boxes per server.
//   - staticStore: Store to keep parsed GTFS static data per server.
//   - maxRetries: Maximum number of retries (with exponential backoff) for each server’s bundle download.

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
// parses it, and stores the resulting static data in the given StaticStore using
// the serverID as the key. Requests are executed with exponential backoff to handle
// transient network errors (e.g., timeouts, connection failures).
//
// It performs the following steps:
//   1. Makes an HTTP GET request (with exponential backoff) to download the GTFS bundle.
//   2. Reads and parses the response body as GTFS static data.
//   3. Stores the parsed data in the StaticStore.
//
// Parameters:
//   - url: The URL of the GTFS static bundle (usually a zip file).
//   - serverID: The identifier used to store and retrieve the static data from the store.
//   - staticStore: The in-memory store that holds GTFS static data indexed by server ID.
//   - maxRetries: The maximum number of retry attempts allowed during exponential backoff
//                 before giving up on reaching the server
//
// Returns:
//   - gtfs static data
//   - error: Describes what went wrong, or nil if the operation was successful.

func downloadGTFSBundle(ctx context.Context, url, agencyID string, maxRetries int) (*remoteGtfs.Static, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	sanitizedURL := utils.SanitizeServerURL(url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = fmt.Errorf("failed to create request for %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": sanitizedURL,
			},
		})
		return nil, err
	}

	resp, err := config.DoWithBackoff(ctx, client, req, maxRetries)

	if err != nil {
		err = fmt.Errorf("failed to make GET request to %s: %w", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": sanitizedURL,
			},
		})
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("unexpected response status %d when downloading GTFS bundle from %s", resp.StatusCode, url)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url":    sanitizedURL,
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
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": sanitizedURL,
			},
		})
		return nil, err
	}
	return staticBundle, nil

}

// storeGTFSBundles merges parsed GTFS static bundles and stores the combined
// result in memory, computing a bounding box over all stops.
//
// The function performs the following:
//   1. Wraps each GTFS static bundle into a StaticData object, keeping only the
//      relevant parts needed by the application to avoid storing the full
//      bundle in memory.
//   2. Merges stops and agencies across bundles, deduplicating by stop/agency ID,
//      and appends all service entries.
//   3. Computes the bounding box from all merged stops.
//   4. Stores the merged StaticData, fetch time, and bounding box in the stores,
//      keyed by agency ID.
//
// Parameters:
//   - staticBundles: The parsed GTFS static bundles to merge.
//   - agencyID: The identifier used to store and retrieve data for a specific agency.
//   - staticStore: The in-memory store holding GTFS static data indexed by agency ID.
//   - boundingBoxStore: The in-memory store holding computed bounding boxes for GTFS data.
//
// Returns:
//   - error: If computing the bounding box fails, an error is returned. Otherwise, nil.

func storeGTFSBundles(staticBundles []*remoteGtfs.Static, agencyID string, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore) error {
	staticData := &models.StaticData{}
	stops := make(map[string]struct{})
	agencies := make(map[string]struct{})
	for _, staticBundle := range staticBundles {
		data := models.NewStaticData(staticBundle)
		for _, stop := range data.Stops {
			if _, exists := stops[stop.Id]; !exists {
				staticData.Stops = append(staticData.Stops, stop)
				stops[stop.Id] = struct{}{}
			}
		}
		for _, agency := range data.Agencies {
			if _, exists := agencies[agency.Id]; !exists {
				staticData.Agencies = append(staticData.Agencies, agency)
				agencies[agency.Id] = struct{}{}
			}
		}
		staticData.Services = append(staticData.Services, data.Services...)
	}
	bbox, err := geo.ComputeBoundingBox(staticData.Stops)
	if err != nil {
		return fmt.Errorf("could not compute bounding box for agency_id %s: %w", agencyID, err)
	}
	staticStore.Set(agencyID, staticData)
	staticStore.SetFetchTime(agencyID, time.Now().UTC())
	boundingBoxStore.Set(agencyID, bbox)
	return nil
}

func storeGTFSBundle(staticBundle *remoteGtfs.Static, agencyID string, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore) error {
	return storeGTFSBundles([]*remoteGtfs.Static{staticBundle}, agencyID, staticStore, boundingBoxStore)
}

// getStopLocationsByIDs retrieves stop locations by their IDs from the GTFS cache.
// It returns a map of stop IDs to gtfs.Stop objects.

func getStopLocationsByIDs(agencyID string, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	staticData, ok := staticStore.Get(agencyID)
	if !ok || staticData == nil {
		err := fmt.Errorf("no GTFS static data found for agency ID %s", agencyID)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
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
	merged := &models.RealtimeData{}
	vehicleIDs := make(map[string]struct{})
	for _, feed := range server.GtfsRTFeeds {
		sanitizedURL := utils.SanitizeServerURL(feed.VehiclePositionURL)
		req, err := http.NewRequest(http.MethodGet, feed.VehiclePositionURL, nil)
		if err != nil {
			err = fmt.Errorf("create GTFS-RT request: %w", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("agency_id", server.AgencyID),
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return err
		}
		if feed.GtfsRTAPIKey != "" {
			req.Header.Set(feed.GtfsRTAPIKey, feed.GtfsRTAPIValue)
		}
		resp, err := client.Do(req)
		if err != nil {
			err = fmt.Errorf("fetch GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("agency_id", server.AgencyID),
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return err
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			err = fmt.Errorf("read GTFS-RT feed %s: %w", feed.VehiclePositionURL, readErr)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("agency_id", server.AgencyID),
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return err
		}
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GTFS-RT feed %s returned %s", feed.VehiclePositionURL, resp.Status)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("agency_id", server.AgencyID),
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
					"status":               resp.Status,
				},
			})
			return err
		}
		parsed, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
		if err != nil {
			err = fmt.Errorf("parse GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: utils.MakeMap("agency_id", server.AgencyID),
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return err
		}
		for _, vehicle := range parsed.Vehicles {
			id := ""
			if vehicle.ID != nil {
				id = vehicle.ID.ID
			}
			if id != "" {
				if _, exists := vehicleIDs[id]; exists {
					continue
				}
				vehicleIDs[id] = struct{}{}
			}
			merged.Vehicles = append(merged.Vehicles, vehicle)
		}
	}
	realtimeStore.Set(server.AgencyID, merged)
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
