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
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// StaticBundleObserver is invoked once per (server, agency) tuple after the
// gtfs service stores a freshly-parsed static bundle. The observer can then
// emit introspection metrics (counts, attribution status, etc.) without the
// gtfs package needing to import the metrics package.
//
// Observers must be cheap and non-blocking; they run on the goroutine that
// completed the static download. A panicking observer is recovered.
//
// The observer is optional; nil means "do nothing extra".
type StaticBundleObserver func(server models.ObaServer, agencyID, agencyName string, bundle *models.StaticData)

// downloadGTFSBundles fetches and processes GTFS static bundles concurrently for a list of OBA servers.
//
// Each server entry spawns its own goroutine that:
//  1. Downloads every configured GTFS static feed (with backoff retries).
//  2. Discovers the agencies each feed declares via agency.txt — multiple
//     agencies per feed are accepted (one bundle pointer-shared across serverKeys).
//  3. Merges the feeds into one StaticData per server.
//  4. Stores the merged bundle under (oba_base_url, agency_id) for every
//     declared agency, and computes the bounding box per agency.
//  5. Populates the RouteAgencyIndex with route_id → agency_id mappings so the
//     RT metrics can attribute vehicles by their TripDescriptor.route_id.
//
// Server-mode vs. agency-mode:
//
//   - Agency-mode: server.AgencyID is non-empty. We still iterate agency.txt
//     to learn each route's owning agency (for vehicle attribution), but the
//     bundle is stored under server.ServerKey() = (oba_base_url, agency_id)
//     exactly once.
//   - Server-mode: server.AgencyID is empty. agency.txt is the SOLE source of
//     agency identity. The bundle is stored once per declared agency_id,
//     pointer-shared across serverKeys. If agency.txt is empty or has zero
//     rows with agency_id populated, the bundle is skipped with a Sentry warn.
//
// Concurrency: one goroutine per server, sync.WaitGroup to join.
//
// Errors are reported per-server; one bad entry never blocks another.
func downloadGTFSBundles(ctx context.Context, client *http.Client, servers []models.ObaServer, logger *slog.Logger, boundingBoxStore *geo.BoundingBoxStore, staticStore *StaticStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, maxRetries int) {
	var wg sync.WaitGroup
	for _, server := range servers {
		s := server
		wg.Add(1)
		go func() {
			defer wg.Done()
			bundles := make([]*remoteGtfs.Static, 0, len(s.GtfsStaticFeeds))
			for _, gtfsURL := range s.GtfsStaticFeeds {
				staticBundle, err := downloadGTFSBundle(ctx, client, gtfsURL, s.AgencyID, maxRetries)
				if err == nil {
					bundles = append(bundles, staticBundle)
					continue
				}
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: map[string]string{
						"agency_id":   s.AgencyID,
						"server_name": s.ServerName,
					},
					ExtraContext: map[string]interface{}{
						"gtfs_url": gtfsURL,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to download GTFS bundle", "agency_id", s.AgencyID, "server_name", s.ServerName, "gtfs_url", gtfsURL, "error", err)
				continue
			}
			if len(bundles) == 0 {
				logger.Error("No GTFS bundles downloaded", "server_name", s.ServerName, "agency_id", s.AgencyID)
				return
			}
			if err := storeStaticForServer(s, bundles, staticStore, boundingBoxStore, routeAgencyIndex, observer, logger); err != nil {
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags: map[string]string{
						"agency_id":   s.AgencyID,
						"server_name": s.ServerName,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to store GTFS bundles", "agency_id", s.AgencyID, "server_name", s.ServerName, "error", err)
			}
		}()
	}
	wg.Wait()
}

// storeStaticForServer merges the parsed bundles for one server and stores the
// result under one serverKey (severKey is a string = oba_base_url - agency_id)
// per declared agency. Multi-agency feeds are
// supported: a single bundle pointer is registered under multiple serverKeys
// (one per agency_id row in agency.txt).
//
// The route → agency index is populated from each bundle's routes.txt. Every
// route_id encountered is mapped to its owning agency_id, and the agency_name
// is recorded for human-readable labels later.
//
// observer, if non-nil, is invoked once per (server, agency) tuple after the
// store call so the metrics layer can emit introspection gauges without the
// gtfs package needing to import it.
func storeStaticForServer(server models.ObaServer, bundles []*remoteGtfs.Static, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, logger *slog.Logger) error {
	mergedbundle, declaredAgencies := mergeStaticAndDiscoverAgencies(bundles)

	// Agency-mode: the operator named the agency, so the bundle is stored
	// exactly once under server.ServerKey() — the same key every agency-mode
	// reader (checkBundleExpiration, getStopLocationsByIDs, the bbox lookup)
	// derives from the configured entry. Deriving the key from agency.txt
	// instead would silently miss whenever the feed's agency_id differs from
	// the configured one, or is blank — which is legal for a single-agency
	// feed and would leave nothing stored at all.
	storageAgencies := declaredAgencies
	if !server.IsServerScoped() {
		agencyName := server.AgencyName
		for _, declared := range declaredAgencies {
			if declared.AgencyID == server.AgencyID && declared.AgencyName != "" {
				agencyName = declared.AgencyName
				break
			}
		}
		storageAgencies = []declaredAgency{{AgencyID: server.AgencyID, AgencyName: agencyName}}
	} else if len(declaredAgencies) == 0 {
		logger.Warn("No agency_id declared in any static feed for server; skipping per-agency storage",
			"server_name", server.ServerName,
			"oba_base_url", server.ObaBaseURL)
		report.ReportErrorWithSentryOptions(
			fmt.Errorf("server %q (%s): no agency_id declared in any static feed", server.ServerName, server.ObaBaseURL),
			report.SentryReportOptions{
				Tags:  utils.MakeMap("server_name", server.ServerName),
				Level: sentry.LevelWarning,
			},
		)
		return nil
	}

	// Keep the server-wide box for the server-scoped vehicle pass and as a
	// fallback when an agency has no usable stops. Agency boxes are computed
	// from the raw feeds before merge deduplicates stops, while the merged
	// StaticData remains the single shared pointer stored below.
	unionBox, unionBoxErr := geo.ComputeBoundingBox(mergedbundle.Stops)
	agencyBoxes := computeAgencyBoundingBoxes(bundles)

	// Per-agency storage. The merged StaticData is pointer-shared across all
	// serverKeys — one allocation regardless of how many agencies the server
	// serves. Memory cost stays O(bundles) not O(bundles × agencies).
	for _, declaredAgency := range storageAgencies {
		serverKey := models.ServerKey(server.ObaBaseURL, declaredAgency.AgencyID)
		staticStore.Set(serverKey, mergedbundle)
		staticStore.SetFetchTime(serverKey, time.Now().UTC())

		bbox, ok := agencyBoxes[declaredAgency.AgencyID]
		if !ok {
			if unionBoxErr != nil {
				logger.Error("Could not compute bounding box", "server_key", serverKey, "error", unionBoxErr)
				continue
			}
			logger.Warn("No stops associated with agency; using server-wide bounding box",
				"server_key", serverKey, "agency_id", declaredAgency.AgencyID)
			bbox = unionBox
		}
		boundingBoxStore.Set(serverKey, bbox)
		// The server-scoped key intentionally remains the union box because
		// the vehicle pass uses it for unattributed vehicles.
		if server.IsServerScoped() && unionBoxErr == nil {
			boundingBoxStore.Set(models.ServerKey(server.ObaBaseURL, ""), unionBox)
		}

		if observer != nil {
			func() {
				defer func() {
					_ = recover() // observers must not bring down the download path
				}()
				observer(server, declaredAgency.AgencyID, declaredAgency.AgencyName, mergedbundle)
			}()
		}
	}

	// Populate the per-server route → agency index. We build it in two passes:
	// first we collect every (route_id, agency_id) pair from routes.txt, then we
	// hand the whole map to the index in one Set call so the read lock isn't
	// taken between individual writes.
	routeMap := make(map[string]string, len(mergedbundle.Routes))
	for _, route := range mergedbundle.Routes {
		if route.Id == "" {
			continue
		}
		agencyID := agencyIDFromRoute(route)
		if agencyID == "" {
			continue
		}
		routeMap[route.Id] = agencyID
	}
	routeAgencyIndex.Set(server.ObaBaseURL, routeMap)
	for _, decl := range declaredAgencies {
		routeAgencyIndex.SetAgencyName(server.ObaBaseURL, decl.AgencyID, decl.AgencyName)
	}
	for _, decl := range storageAgencies {
		routeAgencyIndex.SetAgencyName(server.ObaBaseURL, decl.AgencyID, decl.AgencyName)
	}

	return nil
}

// computeAgencyBoundingBoxes computes one bounding box per agency from the
// static feeds that declare that agency. All stops in such a feed contribute to
// the agency's box, including feeds that declare multiple agencies. The raw
// feed stops are used rather than merged stops so a stop-id collision in
// another feed does not remove that feed's location from this calculation.
//
// The returned boxes are transient; the static store continues to hold one
// merged StaticData pointer shared by every agency.
func computeAgencyBoundingBoxes(bundles []*remoteGtfs.Static) map[string]geo.BoundingBox {
	stopsByAgency := make(map[string]map[string]remoteGtfs.Stop)
	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}
		for _, agency := range bundle.Agencies {
			if agency.Id == "" {
				continue
			}
			stops, ok := stopsByAgency[agency.Id]
			if !ok {
				stops = make(map[string]remoteGtfs.Stop)
				stopsByAgency[agency.Id] = stops
			}
			for _, stop := range bundle.Stops {
				if _, exists := stops[stop.Id]; !exists {
					stops[stop.Id] = stop
				}
			}
		}
	}

	boxes := make(map[string]geo.BoundingBox, len(stopsByAgency))
	for agencyID, stopsByID := range stopsByAgency {
		stops := make([]remoteGtfs.Stop, 0, len(stopsByID))
		for _, stop := range stopsByID {
			stops = append(stops, stop)
		}
		if bbox, err := geo.ComputeBoundingBox(stops); err == nil {
			boxes[agencyID] = bbox
		}
	}
	return boxes
}

// declaredAgency is the agency identity recovered from agency.txt during merge.
type declaredAgency struct {
	AgencyID   string
	AgencyName string
}

// mergeStaticAndDiscoverAgencies merges parsed bundles into one StaticData
// while extracting the set of declared agencies from agency.txt. Returns the
// merged bundle (nil if no input) and the declared-agency list.
//
// Multi-agency feeds are accepted: if a feed's agency.txt declares agencies
// A and B, both end up in the returned list. Duplicate (agency_id, agency_name,
// agency_url) rows across feeds collapse silently — the first occurrence
// wins. Collisions where the identity fields disagree are reported to Sentry
// at warning level (see Change 2 below) but the kept entry is still the
// first occurrence.
//
// Stop-id collisions (same stop_id at different lat/lon) are also reported
// to Sentry at warning level; the first occurrence wins (see Change 1
// below).
func mergeStaticAndDiscoverAgencies(bundles []*remoteGtfs.Static) (*models.StaticData, []declaredAgency) {
	if len(bundles) == 0 {
		return &models.StaticData{}, nil
	}
	staticData := &models.StaticData{}
	stopsByID := make(map[string]stopLocation)
	agenciesByID := make(map[string]agencyIdentity)
	for _, staticBundle := range bundles {
		if staticBundle == nil {
			continue
		}
		data := models.NewStaticData(staticBundle)
		for _, stop := range data.Stops {
			existing, exists := stopsByID[stop.Id]
			if !exists {
				staticData.Stops = append(staticData.Stops, stop)
				stopsByID[stop.Id] = stopLocation{lat: stop.Latitude, lon: stop.Longitude}
				continue
			}
			if sameStopLocation(existing.lat, existing.lon, stop.Latitude, stop.Longitude) {
				// Exact duplicate (same id, same location). Silent skip.
				continue
			}
			// stop_id collision with different location — warn and skip.
			report.ReportErrorWithSentryOptions(
				fmt.Errorf("static bundle has a duplicate stop_id %q at a different location; existing=(lat=%s, lon=%s), duplicate=(lat=%s, lon=%s); keeping first occurrence",
					stop.Id,
					formatLatLon(existing.lat), formatLatLon(existing.lon),
					formatLatLon(stop.Latitude), formatLatLon(stop.Longitude)),
				report.SentryReportOptions{
					Tags: map[string]string{"stop_id": stop.Id},
					ExtraContext: map[string]interface{}{
						"existing_lat":  existing.lat,
						"existing_lon":  existing.lon,
						"duplicate_lat": stop.Latitude,
						"duplicate_lon": stop.Longitude,
					},
					Level: sentry.LevelWarning,
				},
			)
			// NOTE: Today Watchdog keeps the first occurrence and silently drops
			// the duplicate's location. Until stop-collision support lands, please
			// make sure your multi-feed configuration has no stop_id collisions —
			// in practice that means using a feed bundle that has already been
			// merged outside Watchdog rather than supplying multiple per-agency
			// feeds to be combined at runtime.
			//
			// TODO: Support stop_id collisions across feeds by storing the
			// conflicting locations and emitting per-(agency, stop_id) series
			// for the bbox check and unmatched-stop resolution paths. Today we
			// drop the duplicate's location entirely, so the second physical
			// location is invisible to operators.
		}
		for _, agency := range data.Agencies {
			if agency.Id == "" {
				continue
			}
			existing, exists := agenciesByID[agency.Id]
			if !exists {
				agenciesByID[agency.Id] = agencyIdentity{Name: agency.Name, Url: agency.Url}
				staticData.Agencies = append(staticData.Agencies, agency)
				continue
			}
			if existing.Name == agency.Name && existing.Url == agency.Url {
				// Exact duplicate (same id, name, url). Silent skip.
				continue
			}
			// agency_id collision with mismatching identity — warn.
			report.ReportErrorWithSentryOptions(
				fmt.Errorf("static bundle has a duplicate agency_id %q with mismatching identity; existing=(name=%q, url=%q), duplicate=(name=%q, url=%q); keeping first occurrence",
					agency.Id, existing.Name, existing.Url, agency.Name, agency.Url),
				report.SentryReportOptions{
					Tags: map[string]string{"agency_id": agency.Id},
					ExtraContext: map[string]interface{}{
						"existing_name":  existing.Name,
						"existing_url":   existing.Url,
						"duplicate_name": agency.Name,
						"duplicate_url":  agency.Url,
					},
					Level: sentry.LevelWarning,
				},
			)
			// Keep first occurrence — do NOT overwrite the map entry or the
			// kept agency object in staticData.Agencies.
		}
		// Services are appended without deduplication, unlike stops and agencies above.
		// Stops and agencies are keyed and addressed individually by ID later, so
		// duplicates would cause collisions and must be collapsed (first occurrence wins).
		// Service entries, by contrast, are only ever collapsed into aggregate ranges
		// (e.g. earliest/latest service dates for bundle-expiration checks), so
		// duplicates are a no-op — and deduplicating by service ID could actually drop a
		// legitimately different date range from another feed of the same agency.
		staticData.Services = append(staticData.Services, data.Services...)
		staticData.Routes = append(staticData.Routes, data.Routes...)
	}

	declared := make([]declaredAgency, 0, len(agenciesByID))
	for id, ident := range agenciesByID {
		declared = append(declared, declaredAgency{AgencyID: id, AgencyName: ident.Name})
	}
	return staticData, declared
}

// agencyIDFromRoute returns the agency_id of the agency that owns a route.
// Returns "" when the route or its agency is missing — callers should treat
// that as "unattributable" and either skip the route or report it.
func agencyIDFromRoute(route remoteGtfs.Route) string {
	if route.Agency == nil {
		return ""
	}
	return route.Agency.Id
}

// refreshGTFSBundles periodically refreshes GTFS static bundles for a list of OBA servers.
//
// It runs in a loop, triggered at the specified interval, and performs the following:
//  1. Logs the refresh operation.
//  2. Calls downloadGTFSBundles to fetch, parse, and store updated GTFS data for all servers.
//     Each server's bundle download uses exponential backoff with retries, up to maxRetries attempts.
//  3. Updates geographic bounding boxes based on the downloaded data.
//
// The function listens to context cancellation (`ctx.Done()`) to gracefully stop the refresh routine.
// servers is a supplier rather than a slice because the configuration can
// change while the routine runs (--config-url). Capturing the boot-time slice
// meant a server added later never had its bundle downloaded at all, and one
// removed later kept being fetched.
func refreshGTFSBundles(ctx context.Context, client *http.Client, servers func() []models.ObaServer, logger *slog.Logger, interval time.Duration, boundingBoxstore *geo.BoundingBoxStore, staticStore *StaticStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, maxRetries int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping GTFS bundle refresh routine")
			return
		case <-ticker.C:
			logger.Info("Refreshing GTFS bundles")
			downloadGTFSBundles(ctx, client, servers(), logger, boundingBoxstore, staticStore, routeAgencyIndex, observer, maxRetries)
		}
	}
}

// downloadGTFSBundle fetches a GTFS static bundle from the provided URL and
// parses it. The agencyID argument is only used as a tag for Sentry error
// reports; the bundle itself is not keyed by it (that happens later in
// storeStaticForServer after agency.txt has been parsed).
//
// Requests use exponential backoff to handle transient network errors
// (e.g., timeouts, connection failures).
func downloadGTFSBundle(ctx context.Context, client *http.Client, url, agencyID string, maxRetries int) (*remoteGtfs.Static, error) {
	sanitizedURL := utils.SanitizeServerURL(url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
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

	resp, err := utils.DoWithBackoff(ctx, client, req, maxRetries)
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

// getStopLocationsByIDs retrieves stop locations by their IDs from the GTFS cache.
// It returns a map of stop IDs to gtfs.Stop objects.
func getStopLocationsByIDs(serverKey string, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	staticData, ok := staticStore.Get(serverKey)
	if !ok || staticData == nil {
		err := fmt.Errorf("no GTFS static data found for server key %s", serverKey)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"server_key": serverKey},
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
		return time.Time{}, time.Time{}, fmt.Errorf("no services found in static data")
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

// fetchAndStoreGTFSRTFeed fetches the GTFS-Realtime feed(s) for a single OBA
// server, parses the vehicle positions, and stores the merged result in the
// RealtimeStore under server.ServerKey().
//
// One key, one fetch, per tick. For a server-scoped entry the key is the
// server-scoped one (empty agency_id), because the vehicle pass reads the
// merged feed once for the whole server and attributes each vehicle to its
// owning agency through the route -> agency index. Registering the same feed
// under every agency's key would only invite a per-agency pass, which
// double-counts.
//
// A server may expose multiple GTFS-RT feeds. Each feed is treated as an
// independent vehicle namespace: GTFS-RT vehicle IDs are only unique within
// a single feed, so two feeds that both report vehicle "101" refer to two
// distinct physical vehicles and are BOTH retained. Deduplication only
// guards against repeats within one feed (a malformed feed repeating an ID).
// Every retained vehicle is tagged with the zero-based index of the feed it
// came from (see models.RealtimeVehicle.FeedID) so consumers can key
// per-vehicle identity on the (feed, vehicle_id) pair.
func fetchAndStoreGTFSRTFeed(server models.ObaServer, realtimeStore *RealtimeStore, client *http.Client) error {
	merged, err := parseGTFSRTFeeds(server, client)
	if err != nil {
		return err
	}
	if merged == nil {
		return nil
	}
	realtimeStore.Set(server.ServerKey(), merged)
	return nil
}

// parseGTFSRTFeeds performs the actual HTTP fetch + protobuf parse for every
// RT feed the server exposes, returning the merged *models.RealtimeData.
// Errors at any stage short-circuit and surface to the caller; the merged
// pointer is always non-nil so the caller can register it under storeKeys
// even on a partially-populated parse.
//
// A server may expose multiple GTFS-RT feeds. Each feed is treated as an
// independent vehicle namespace: GTFS-RT vehicle IDs are only unique within
// a single feed, so two feeds that both report vehicle "101" refer to two
// distinct physical vehicles and are BOTH retained. Deduplication only
// guards against repeats within one feed (a malformed feed repeating an ID).
// Every retained vehicle is tagged with the zero-based index of the feed it
// came from (see models.RealtimeVehicle.FeedID) so consumers can key
// per-vehicle identity on the (feed, vehicle_id) pair.
func parseGTFSRTFeeds(server models.ObaServer, client *http.Client) (*models.RealtimeData, error) {
	merged := &models.RealtimeData{}
	for feedIdx, feed := range server.GtfsRTFeeds {
		feedID := fmt.Sprintf("%d", feedIdx)
		sanitizedURL := utils.SanitizeServerURL(feed.VehiclePositionURL)
		req, err := http.NewRequest(http.MethodGet, feed.VehiclePositionURL, nil)
		if err != nil {
			err = fmt.Errorf("create GTFS-RT request: %w", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return nil, err
		}
		if feed.GtfsRTAPIKey != "" {
			req.Header.Set(feed.GtfsRTAPIKey, feed.GtfsRTAPIValue)
		}
		resp, err := client.Do(req)
		if err != nil {
			err = fmt.Errorf("fetch GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return nil, err
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			err = fmt.Errorf("read GTFS-RT feed %s: %w", feed.VehiclePositionURL, readErr)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GTFS-RT feed %s returned %s", feed.VehiclePositionURL, resp.Status)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
					"status":               resp.Status,
				},
			})
			return nil, err
		}
		parsed, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
		if err != nil {
			err = fmt.Errorf("parse GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return nil, err
		}
		vehicleIDs := make(map[string]struct{})
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
			merged.Vehicles = append(merged.Vehicles, models.RealtimeVehicle{Vehicle: vehicle, FeedID: feedID})
		}
	}
	return merged, nil
}
