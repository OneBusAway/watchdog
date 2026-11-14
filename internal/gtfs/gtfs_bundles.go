package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfscsv "github.com/OneBusAway/go-gtfs/csv"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"github.com/getsentry/sentry-go"
	"google.golang.org/protobuf/proto"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// StaticBundleObserver is invoked once per (server, agency) tuple after the
// gtfs service stores a freshly-parsed static bundle. The observer can then
// emit introspection metrics (counts, feed mappings, etc.) without the
// gtfs package needing to import the metrics package.
//
// Observers must be cheap and non-blocking; they run on the goroutine that
// completed the static download. A panicking observer is recovered.
//
// The observer is optional; nil means "do nothing extra".
type StaticBundleObserver func(server models.ObaServer, agencyID, agencyName string, bundle *models.StaticData)

// StaticFeedMappingObserver receives source-feed provenance after a static
// refresh has parsed and stored at least one configured feed. Results contains
// only feeds successfully fetched and parsed; ConfiguredFeedURLs lets the
// observer retire state for feeds removed from configuration without erasing
// the last known result for a temporarily unavailable feed.
type StaticFeedMappingObserver func(server models.ObaServer, observation StaticFeedMappingObservation)

type StaticFeedMappingObservation struct {
	ConfiguredFeedURLs []string
	Results            []StaticFeedMappingResult
}

type StaticFeedMappingResult struct {
	FeedURL  string
	Mappings []StaticFeedAgencyMapping
	Failures []StaticFeedMappingFailure
}

type StaticFeedAgencyMapping struct {
	AgencyID   string
	AgencyName string
}

type StaticFeedMappingFailureReason string

const (
	StaticFeedMappingMissingAgency   StaticFeedMappingFailureReason = "missing_agency"
	StaticFeedMappingAmbiguousAgency StaticFeedMappingFailureReason = "ambiguous_agency"
	StaticFeedMappingUnknownAgency   StaticFeedMappingFailureReason = "unknown_agency"
)

type StaticFeedMappingFailure struct {
	Reason StaticFeedMappingFailureReason
}

type downloadedStaticFeed struct {
	url    string
	data   []byte
	bundle *remoteGtfs.Static
}

type StaticRefreshResult struct {
	Server      models.ObaServer
	AttemptedAt time.Time
	Err         error
}

// downloadGTFSBundles fetches and processes GTFS static bundles concurrently for a list of OBA servers.
//
// Each server entry spawns its own goroutine that:
//  1. Downloads the upstream-merged static feed (with backoff retries).
//  2. In agency-mode, builds and stores an owned agency-scoped static snapshot.
//  3. In server-mode, stores the artifact's shared StaticData under every
//     declared agency key.
//  4. Computes the appropriate bounding boxes for the selected mode.
//  5. Populates RouteAgencyIndex with route_id and trip_id attribution maps.
//
// Server-mode vs. agency-mode:
//
//   - Agency-mode: server.AgencyID is non-empty. Only static data resolved to
//     that configured agency is stored under server.ServerKey().
//   - Server-mode: server.AgencyID is empty. agency.txt is the SOLE source of
//     agency identity. The bundle is stored once per declared agency_id,
//     pointer-shared across serverKeys. If agency.txt is empty or has zero
//     rows with agency_id populated, the bundle is skipped with a Sentry warn.
//
// Concurrency: one goroutine per server, sync.WaitGroup to join.
//
// Errors are reported per-server; one bad entry never blocks another.
func downloadGTFSBundles(ctx context.Context, client *http.Client, servers []models.ObaServer, logger *slog.Logger, boundingBoxStore *geo.BoundingBoxStore, staticStore *StaticStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, mappingObserver StaticFeedMappingObserver, maxRetries int) []StaticRefreshResult {
	var wg sync.WaitGroup
	results := make(chan StaticRefreshResult, len(servers))
	for _, server := range servers {
		s := server
		if !staticStore.IsConfigured(s) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			attemptedAt := time.Now().UTC()
			finish := func(err error) {
				results <- StaticRefreshResult{Server: s, AttemptedAt: attemptedAt, Err: err}
			}
			scheduleStore := staticStore.ScheduleStore()
			markScheduleUnavailable := func() {
				staticStore.WithRefreshLock(func() {
					if staticStore.IsConfigured(s) {
						scheduleStore.MarkUnavailable(s)
					}
				})
			}
			if len(s.GtfsStaticFeeds) != 1 {
				err := fmt.Errorf("expected one upstream-merged static feed; merge feeds upstream")
				markScheduleUnavailable()
				finish(err)
				return
			}
			gtfsURL := s.GtfsStaticFeeds[0]
			bundle, rawData, err := downloadGTFSBundleData(ctx, client, gtfsURL, s.AgencyID, maxRetries)
			if err != nil {
				markScheduleUnavailable()
				report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
					Tags:         map[string]string{"agency_id": s.AgencyID, "server_name": s.ServerName},
					ExtraContext: map[string]interface{}{"gtfs_url": utils.SanitizeServerURL(gtfsURL)},
					Level:        sentry.LevelError,
				})
				logger.Error("Failed to download GTFS bundle; preserving prior complete snapshot", "agency_id", s.AgencyID, "server_name", s.ServerName, "error", err)
				finish(err)
				return
			}
			bundles := []*remoteGtfs.Static{bundle}
			downloaded := []downloadedStaticFeed{{url: gtfsURL, data: rawData, bundle: bundle}}

			schedules, scheduleErr := compileSchedules(s, downloaded)
			if scheduleErr != nil {
				markScheduleUnavailable()
				logger.Error("GTFS schedule compilation unavailable", "server_name", s.ServerName, "agency_id", s.AgencyID, "error", scheduleErr)
				report.ReportErrorWithSentryOptions(scheduleErr, report.SentryReportOptions{
					Tags:  map[string]string{"agency_id": s.AgencyID, "server_name": s.ServerName},
					Level: sentry.LevelWarning,
				})
				finish(scheduleErr)
				return
			}
			configured := false
			var storeErr error
			staticStore.WithRefreshLock(func() {
				if !staticStore.IsConfigured(s) {
					return
				}
				configured = true
				storeErr = storeStaticForServer(s, bundles, staticStore, boundingBoxStore, routeAgencyIndex, observer, logger)
				if storeErr != nil {
					scheduleStore.MarkUnavailable(s)
					return
				}
				scheduleStore.Replace(s, schedules)
				if mappingObserver != nil {
					func() {
						defer func() { _ = recover() }()
						mappingObserver(s, classifyStaticFeedMappings(s, downloaded))
					}()
				}
			})
			if !configured {
				return
			}
			if storeErr != nil {
				sum := sha256.Sum256(rawData)
				failure := staticPublicationFailure(storeErr, gtfsURL, hex.EncodeToString(sum[:]))
				report.ReportErrorWithSentryOptions(failure, report.SentryReportOptions{
					Tags: map[string]string{
						"agency_id":   s.AgencyID,
						"server_name": s.ServerName,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to store GTFS bundles", "agency_id", s.AgencyID, "server_name", s.ServerName, "error", storeErr)
				finish(failure)
				return
			}
			finish(nil)
		}()
	}
	wg.Wait()
	close(results)
	completed := make([]StaticRefreshResult, 0, len(servers))
	for result := range results {
		completed = append(completed, result)
	}
	return completed
}

func classifyStaticFeedMappings(server models.ObaServer, feeds []downloadedStaticFeed) StaticFeedMappingObservation {
	observation := StaticFeedMappingObservation{
		ConfiguredFeedURLs: make([]string, 0, len(server.GtfsStaticFeeds)),
		Results:            make([]StaticFeedMappingResult, 0, len(feeds)),
	}
	for _, feedURL := range server.GtfsStaticFeeds {
		observation.ConfiguredFeedURLs = append(observation.ConfiguredFeedURLs, utils.SanitizeServerURL(feedURL))
	}

	for _, feed := range feeds {
		result := StaticFeedMappingResult{FeedURL: utils.SanitizeServerURL(feed.url)}
		if feed.bundle == nil || len(feed.bundle.Agencies) == 0 {
			result.Failures = append(result.Failures, StaticFeedMappingFailure{Reason: StaticFeedMappingMissingAgency})
			observation.Results = append(observation.Results, result)
			continue
		}

		if !server.IsServerScoped() {
			matched := false
			blank := 0
			for _, agency := range feed.bundle.Agencies {
				if agency.Id == "" {
					blank++
					continue
				}
				if agency.Id == server.AgencyID && !matched {
					name := agency.Name
					if name == "" {
						name = server.AgencyName
					}
					result.Mappings = append(result.Mappings, StaticFeedAgencyMapping{AgencyID: server.AgencyID, AgencyName: name})
					matched = true
				}
			}
			if len(feed.bundle.Agencies) == 1 && blank == 1 {
				result.Mappings = append(result.Mappings, StaticFeedAgencyMapping{AgencyID: server.AgencyID, AgencyName: server.AgencyName})
			} else if blank > 0 {
				result.Failures = append(result.Failures, StaticFeedMappingFailure{Reason: StaticFeedMappingAmbiguousAgency})
			} else if !matched {
				result.Failures = append(result.Failures, StaticFeedMappingFailure{Reason: StaticFeedMappingUnknownAgency})
			}
			observation.Results = append(observation.Results, result)
			continue
		}

		seen := make(map[string]bool)
		blank := 0
		for _, agency := range feed.bundle.Agencies {
			if agency.Id == "" {
				blank++
				continue
			}
			if !seen[agency.Id] {
				seen[agency.Id] = true
				result.Mappings = append(result.Mappings, StaticFeedAgencyMapping{AgencyID: agency.Id, AgencyName: agency.Name})
			}
		}
		if blank > 0 {
			reason := StaticFeedMappingAmbiguousAgency
			if len(feed.bundle.Agencies) == 1 {
				reason = StaticFeedMappingMissingAgency
			}
			result.Failures = append(result.Failures, StaticFeedMappingFailure{Reason: reason})
		}
		observation.Results = append(observation.Results, result)
	}
	return observation
}

// storeStaticForServer stores either an agency-scoped static snapshot or the
// server-mode consolidated bundle under the appropriate composite keys.
//
// Route and trip attribution maps are built from the parser graph. Agency-mode
// publishes them under its configured server key; server-mode publishes them
// under the empty-agency server key.
//
// Server-mode bounding boxes use the upstream artifact's shared stop pool.
// Agency-mode computes its box from the retained scoped stops.
//
// observer, if non-nil, is invoked once per retained (server, agency) tuple.
// A nil bundle retires an agency removed by a complete server-scoped refresh.
func storeStaticForServer(server models.ObaServer, bundles []*remoteGtfs.Static, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, logger *slog.Logger) error {
	if len(bundles) != 1 || bundles[0] == nil {
		return fmt.Errorf("expected one upstream-merged static bundle")
	}
	if err := validateParsedStaticIDs(bundles[0]); err != nil {
		return err
	}
	if !server.IsServerScoped() {
		result := buildAgencyStaticSnapshot(server, bundles, logger)
		serverKey := server.ServerKey()
		if len(result.data.Routes) == 0 {
			// Usually the configured agency_id does not match agency.txt/routes.txt.
			// The snapshot is still stored (filtering is intentional), but the
			// misconfiguration must not be silent: every vehicle will be filtered out.
			err := fmt.Errorf("no routes in static feeds resolve to configured agency_id %q", server.AgencyID)
			logger.Warn("Agency-scoped static snapshot is empty", "server_key", serverKey, "agency_id", server.AgencyID, "error", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags:  map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID},
				Level: sentry.LevelWarning,
			})
		}
		staticStore.ReplaceServerSnapshot(server, map[string]*models.StaticData{serverKey: result.data}, time.Now().UTC())
		routeAgencyIndex.ReplaceCandidates(serverKey, result.routeIDs, result.tripIDs, result.agencyNames)

		if bbox, err := geo.ComputeBoundingBox(result.data.Stops); err == nil {
			boundingBoxStore.Set(serverKey, bbox)
		} else {
			// A successful refresh with no scoped coordinates must not leave a
			// broad box from an older snapshot in place.
			boundingBoxStore.Delete(serverKey)
			logger.Warn("Could not compute agency bounding box", "server_key", serverKey, "agency_id", server.AgencyID, "error", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags:  map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID},
				Level: sentry.LevelWarning,
			})
		}
		if observer != nil {
			func() {
				defer func() { _ = recover() }()
				observer(server, server.AgencyID, result.data.Agencies[0].Name, result.data)
			}()
		}
		return nil
	}

	staticData, declaredAgencies := staticDataAndAgencies(bundles[0])
	computedBoxes := computeBoundingBoxes(bundles)

	storageAgencies := declaredAgencies
	if len(declaredAgencies) == 0 {
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
		return fmt.Errorf("server %q (%s): no agency_id declared in any static feed", server.ServerName, server.ObaBaseURL)
	}

	// Keep the server-wide box for the server-scoped vehicle pass and as a
	// fallback when an agency has no usable stops. Every physical location in
	// the upstream artifact contributes without a persistent per-agency stop index.
	unionBox, unionBoxErr := computedBoxes.union, computedBoxes.unionErr
	agencyBoxes, agencyBoxErrors := computedBoxes.byAgency, computedBoxes.errorsByAgency
	// Per-agency storage. The merged StaticData is pointer-shared across all
	// serverKeys. Only the four transient bounding-box extrema above are kept per
	// agency, so stop storage does not grow with the number of declared agencies.
	snapshots := make(map[string]*models.StaticData, len(storageAgencies))
	for _, declaredAgency := range storageAgencies {
		snapshots[models.ServerKey(server.ObaBaseURL, declaredAgency.AgencyID)] = staticData
	}
	removedKeys := staticStore.ReplaceServerSnapshot(server, snapshots, time.Now().UTC())
	for _, removedKey := range removedKeys {
		boundingBoxStore.Delete(removedKey)
		if observer != nil {
			agencyID := strings.TrimPrefix(removedKey, models.ServerKeyPrefix(server.ObaBaseURL))
			func() {
				defer func() { _ = recover() }()
				observer(server, agencyID, "", nil)
			}()
		}
	}
	for _, declaredAgency := range storageAgencies {
		serverKey := models.ServerKey(server.ObaBaseURL, declaredAgency.AgencyID)

		bbox, ok := agencyBoxes[declaredAgency.AgencyID]
		if !ok {
			agencyBoxErr, agencyBoxFailed := agencyBoxErrors[declaredAgency.AgencyID]
			if unionBoxErr != nil {
				if agencyBoxFailed {
					logger.Error("Could not compute agency bounding box",
						"server_key", serverKey, "agency_id", declaredAgency.AgencyID, "error", agencyBoxErr)
					report.ReportErrorWithSentryOptions(
						fmt.Errorf("server %q (%s): could not compute agency bounding box for %s: %w",
							server.ServerName, server.ObaBaseURL, declaredAgency.AgencyID, agencyBoxErr),
						report.SentryReportOptions{
							Tags:  map[string]string{"server_name": server.ServerName, "agency_id": declaredAgency.AgencyID},
							Level: sentry.LevelError,
						},
					)
				}
				logger.Error("Could not compute server-wide bounding box", "server_key", serverKey, "error", unionBoxErr)
				report.ReportErrorWithSentryOptions(
					fmt.Errorf("server %q (%s): could not compute server-wide bounding box: %w",
						server.ServerName, server.ObaBaseURL, unionBoxErr),
					report.SentryReportOptions{
						Tags:  utils.MakeMap("server_name", server.ServerName),
						Level: sentry.LevelError,
					},
				)
			} else {
				if agencyBoxFailed {
					logger.Warn("Could not compute agency bounding box; using server-wide bounding box",
						"server_key", serverKey, "agency_id", declaredAgency.AgencyID, "error", agencyBoxErr)
					report.ReportErrorWithSentryOptions(
						fmt.Errorf("server %q (%s): could not compute agency bounding box for %s, falling back to union box: %w",
							server.ServerName, server.ObaBaseURL, declaredAgency.AgencyID, agencyBoxErr),
						report.SentryReportOptions{
							Tags:  map[string]string{"server_name": server.ServerName, "agency_id": declaredAgency.AgencyID},
							Level: sentry.LevelWarning,
						},
					)
				} else {
					logger.Warn("No stops associated with agency; using server-wide bounding box",
						"server_key", serverKey, "agency_id", declaredAgency.AgencyID)
				}
				bbox = unionBox
				boundingBoxStore.Set(serverKey, bbox)
			}
		} else {
			boundingBoxStore.Set(serverKey, bbox)
		}
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
				observer(server, declaredAgency.AgencyID, declaredAgency.AgencyName, staticData)
			}()
		}
	}

	// Publish route, trip, and agency-name maps together under the empty-agency
	// server key. The parser graph is still available for trip attribution here.
	routeMap, tripMap := buildAttributionMaps(server, bundles, false, logger)
	names := make(map[string]string, len(declaredAgencies))
	for _, decl := range declaredAgencies {
		names[decl.AgencyID] = decl.AgencyName
	}
	routeAgencyIndex.ReplaceCandidates(server.ServerKey(), routeMap, tripMap, names)

	return nil
}

// computedBoundingBoxes contains the complete transient result of the source-
// feed walk. union covers every stop from every feed and is used by the
// server-scoped vehicle pass and as the server-mode agency fallback. byAgency
// contains each successfully computed agency box, while errorsByAgency records
// agencies that had stops but no usable coordinates. unionErr reports the
// corresponding server-wide failure.
//
// When one pre-merged feed declares several agencies, each server-mode agency
// correctly receives the same box as union because every agency shares that
// feed's stop pool. Only the resulting four extrema are retained per agency.
type computedBoundingBoxes struct {
	union          geo.BoundingBox
	unionErr       error
	byAgency       map[string]geo.BoundingBox
	errorsByAgency map[string]error
}

// computeBoundingBoxes computes transient agency and server bounds. All
// agencies declared in a merged feed share its stop pool in server mode.
func computeBoundingBoxes(bundles []*remoteGtfs.Static) computedBoundingBoxes {
	union := &geo.BoundingBoxAccumulator{}
	byAgency := make(map[string]*geo.BoundingBoxAccumulator)
	for _, bundle := range bundles {
		if bundle == nil {
			continue
		}

		agencyIDs := make(map[string]struct{}, len(bundle.Agencies))
		for _, agency := range bundle.Agencies {
			if agency.Id == "" {
				continue
			}
			agencyIDs[agency.Id] = struct{}{}
			if byAgency[agency.Id] == nil {
				byAgency[agency.Id] = &geo.BoundingBoxAccumulator{}
			}
		}

		for _, stop := range bundle.Stops {
			union.Add(stop)
			for agencyID := range agencyIDs {
				byAgency[agencyID].Add(stop)
			}
		}
	}

	result := computedBoundingBoxes{
		byAgency:       make(map[string]geo.BoundingBox, len(byAgency)),
		errorsByAgency: make(map[string]error),
	}
	result.union, result.unionErr = union.Result()
	for agencyID, accumulator := range byAgency {
		box, err := accumulator.Result()
		if accumulator.StopCount() == 0 {
			continue
		}
		if err != nil {
			result.errorsByAgency[agencyID] = err
			continue
		}
		result.byAgency[agencyID] = box
	}
	return result
}

// declaredAgency is the agency identity declared in the upstream artifact.
type declaredAgency struct {
	AgencyID   string
	AgencyName string
}

// staticDataAndAgencies copies the upstream artifact without resolving IDs.
func staticDataAndAgencies(bundle *remoteGtfs.Static) (*models.StaticData, []declaredAgency) {
	declared := make([]declaredAgency, 0, len(bundle.Agencies))
	for _, agency := range bundle.Agencies {
		if agency.Id != "" {
			declared = append(declared, declaredAgency{AgencyID: agency.Id, AgencyName: agency.Name})
		}
	}
	return models.NewStaticData(bundle), declared
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
type staticRefreshRetryPolicy struct {
	initialDelay time.Duration
	maxDelay     time.Duration
	budget       time.Duration
}

var dailyStaticRefreshRetryPolicy = staticRefreshRetryPolicy{
	initialDelay: 5 * time.Minute,
	maxDelay:     2 * time.Hour,
	budget:       12 * time.Hour,
}

func refreshGTFSBundles(ctx context.Context, client *http.Client, servers func() []models.ObaServer, logger *slog.Logger, interval time.Duration, boundingBoxstore *geo.BoundingBoxStore, staticStore *StaticStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, mappingObserver StaticFeedMappingObserver, refreshObserver StaticRefreshObserver, maxRetries int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping GTFS bundle refresh routine")
			return
		case <-ticker.C:
			logger.Info("Refreshing GTFS bundles")
			runStaticRefreshCampaign(ctx, client, servers, logger, boundingBoxstore, staticStore, routeAgencyIndex, observer, mappingObserver, refreshObserver, maxRetries, dailyStaticRefreshRetryPolicy)
		}
	}
}

func runStaticRefreshCampaign(ctx context.Context, client *http.Client, servers func() []models.ObaServer, logger *slog.Logger, boundingBoxstore *geo.BoundingBoxStore, staticStore *StaticStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, mappingObserver StaticFeedMappingObserver, refreshObserver StaticRefreshObserver, maxRetries int, policy staticRefreshRetryPolicy) {
	pending := append([]models.ObaServer(nil), servers()...)
	deadline := time.Now().Add(policy.budget)
	var delay time.Duration

	for len(pending) > 0 {
		current := make(map[string]models.ObaServer)
		for _, server := range servers() {
			current[server.ServerKey()] = server
		}
		reconciled := pending[:0]
		for _, server := range pending {
			if configured, ok := current[server.ServerKey()]; ok {
				reconciled = append(reconciled, configured)
			}
		}
		pending = reconciled
		if len(pending) == 0 {
			return
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}

		results := downloadGTFSBundles(ctx, client, pending, logger, boundingBoxstore, staticStore, routeAgencyIndex, observer, mappingObserver, maxRetries)
		failed := make([]models.ObaServer, 0, len(results))
		failedResults := make([]StaticRefreshResult, 0, len(results))
		for _, result := range results {
			if !staticStore.IsConfigured(result.Server) {
				continue
			}
			if result.Err == nil {
				if refreshObserver != nil {
					refreshObserver(result.Server, StaticRefreshObservation{AttemptedAt: result.AttemptedAt, Success: true})
				}
				continue
			}
			failed = append(failed, result.Server)
			failedResults = append(failedResults, result)
		}
		if len(failed) == 0 {
			return
		}

		if delay == 0 {
			delay = policy.initialDelay
		} else {
			delay *= 2
			if delay > policy.maxDelay {
				delay = policy.maxDelay
			}
		}
		gaveUp := time.Now().Add(delay).After(deadline)
		for _, result := range failedResults {
			if refreshObserver != nil {
				refreshObserver(result.Server, StaticRefreshObservation{
					AttemptedAt: result.AttemptedAt, Retrying: !gaveUp, GaveUp: gaveUp,
				})
			}
		}
		if gaveUp {
			logger.Error("GTFS static refresh retry campaign exhausted", "failed_servers", len(failed), "retry_budget", policy.budget)
			return
		}
		logger.Warn("GTFS static refresh failed; scheduling retry", "failed_servers", len(failed), "retry_in", delay)
		pending = failed
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
	bundle, _, err := downloadGTFSBundleData(ctx, client, url, agencyID, maxRetries)
	return bundle, err
}

func downloadGTFSBundleData(ctx context.Context, client *http.Client, url, agencyID string, maxRetries int) (*remoteGtfs.Static, []byte, error) {
	data, err := fetchGTFSBundleData(ctx, client, url, agencyID, maxRetries)
	if err != nil {
		return nil, nil, err
	}
	staticBundle, err := parseDownloadedGTFSBundle(parseStaticBundleData, data, url, agencyID)
	if err != nil {
		return nil, nil, err
	}
	return staticBundle, data, nil
}

// fetchGTFSBundleData downloads a feed's bytes, retrying with backoff, without
// parsing them.
func fetchGTFSBundleData(ctx context.Context, client *http.Client, url, agencyID string, maxRetries int) ([]byte, error) {
	sanitizedURL := utils.SanitizeServerURL(url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		err = &StaticFeedError{Stage: StaticFailureRequest, Reason: StaticReasonInvalidURL, Err: fmt.Errorf("failed to create request for %s: %w", url, err)}
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
		err = requestFailure(fmt.Errorf("failed to make GET request to %s: %w", url, err))
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
		err = statusFailure(resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()), fmt.Errorf("unexpected response status %d when downloading GTFS bundle from %s", resp.StatusCode, url))
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url":    sanitizedURL,
				"status": resp.Status,
			},
		})
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStaticFeedDownloadSize+1))
	if err != nil {
		err = &StaticFeedError{Stage: StaticFailureDownload, Reason: StaticReasonConnection, Err: fmt.Errorf("failed to read GTFS bundle response body from %s: %w", url, err)}
		report.ReportError(err)
		return nil, err
	}
	if int64(len(data)) > maxStaticFeedDownloadSize {
		err = &StaticFeedError{Stage: StaticFailureDownload, Reason: StaticReasonResponseTooLarge, Err: fmt.Errorf("GTFS bundle from %s exceeds the %d-byte download limit", url, maxStaticFeedDownloadSize)}
		report.ReportError(err)
		return nil, err
	}

	return data, nil
}

// parseDownloadedGTFSBundle parses a downloaded feed with parse, reporting a
// failure to Sentry.
func parseDownloadedGTFSBundle(parse func(data []byte, url, agencyID string) (*remoteGtfs.Static, error), data []byte, url, agencyID string) (*remoteGtfs.Static, error) {
	staticBundle, err := parse(data, url, agencyID)
	if err != nil {
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": utils.SanitizeServerURL(url),
			},
		})
		return nil, err
	}
	return staticBundle, nil
}

func parseStaticBundleData(data []byte, url, agencyID string) (*remoteGtfs.Static, error) {
	if err := validateStaticIDs(data); err != nil {
		var duplicate *duplicateIDError
		stage, reason := StaticFailureArchive, StaticReasonInvalidZIP
		if errors.As(err, &duplicate) {
			stage, reason = StaticFailureParse, StaticReasonDuplicateID
		}
		sum := sha256.Sum256(data)
		return nil, &StaticFeedError{FeedURL: url, Stage: stage, Reason: reason, ContentHash: hex.EncodeToString(sum[:]), Err: err}
	}
	// Watchdog never reads shapes, and from stop_times.txt needs only which
	// stops each trip serves (agency mode keeps those stops). Skipping the rest
	// cuts Seattle's merged feed from ~580 MB to ~110 MB of peak heap.
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{
		SkipShapes: true,
		StopTimes:  remoteGtfs.StopTimesStopsOnly,
	})
	if err != nil {
		sum := sha256.Sum256(data)
		return nil, &StaticFeedError{Stage: StaticFailureArchive, Reason: StaticReasonInvalidZIP, ContentHash: hex.EncodeToString(sum[:]), Err: fmt.Errorf("failed to parse GTFS static data from %s: %w", url, err)}
	}
	if err := normalizeOmittedAgencyID(data, staticBundle); err != nil {
		sum := sha256.Sum256(data)
		return nil, &StaticFeedError{Stage: StaticFailureAgencyDiscovery, Reason: StaticReasonMissingAgencyFile, ContentHash: hex.EncodeToString(sum[:]), Err: fmt.Errorf("failed to inspect agency.txt in %s for agency %s: %w", url, agencyID, err)}
	}
	return staticBundle, nil
}

// normalizeOmittedAgencyID restores the distinction between an omitted
// agency_id column and an explicitly supplied ID. go-gtfs synthesizes an ID
// for a single agency when the column is absent, but agency-mode needs to treat
// that feed as unqualified so it can associate it with the configured agency.
func normalizeOmittedAgencyID(data []byte, bundle *remoteGtfs.Static) error {
	if bundle == nil || len(bundle.Agencies) != 1 {
		return nil
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if file.Name != "agency.txt" {
			continue
		}
		body, err := file.Open()
		if err != nil {
			return err
		}
		defer body.Close()
		// Use the same BOM-aware reader go-gtfs parsed the file with, so a BOM
		// before a quoted header (or a UTF-16 file) is read identically here.
		header, err := gtfscsv.BOMAwareCSVReader(body).Read()
		if err != nil {
			return err
		}
		for _, column := range header {
			column = strings.TrimPrefix(column, "\ufeff")
			if strings.TrimSpace(column) == "agency_id" {
				return nil
			}
		}
		bundle.Agencies[0].Id = ""
		return nil
	}
	return nil
}

// getStopLocationsByIDs resolves final stop IDs from the accepted static feed.
func getStopLocationsByIDs(serverKey string, stopIDs []string, staticStore *StaticStore) (map[string]remoteGtfs.Stop, error) {
	staticData, ok := staticStore.Get(serverKey)
	if !ok || staticData == nil {
		err := fmt.Errorf("no GTFS static data found for server key %s", serverKey)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"server_key": serverKey},
		})
		return nil, err
	}

	// stopID → struct{} (requested set)
	stopIDSet := make(map[string]struct{}, len(stopIDs))
	for _, id := range stopIDs {
		stopIDSet[id] = struct{}{}
	}

	result := make(map[string]remoteGtfs.Stop)
	for _, stop := range staticData.Stops {
		if _, requested := stopIDSet[stop.Id]; requested {
			if _, alreadyResolved := result[stop.Id]; !alreadyResolved {
				result[stop.Id] = stop
			}
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
		for _, addedDate := range service.AddedDates {
			if addedDate.After(latestEndDate) {
				latestEndDate = addedDate
			}
		}
	}
	return earliestEndDate, latestEndDate, nil
}

// fetchAndStoreGTFSRTFeed publishes one upstream-merged realtime snapshot per
// server key. In agency mode, attribution filters vehicles before publication.
// A failed fetch preserves the last good snapshot and gates the vehicle pass.
func fetchAndStoreGTFSRTFeed(ctx context.Context, server models.ObaServer, realtimeStore *RealtimeStore, client *http.Client, routeAgencyIndex *RouteAgencyIndex) error {
	realtimeStore.ReconcileConfiguration(server)
	observedAt := time.Now().UTC()
	// Config validation requires a source. Internal removal paths still need
	// to retire cached vehicles and let collection clear its freshness series.
	if len(server.GtfsRTFeeds) == 0 {
		realtimeStore.Set(server.ServerKey(), &models.RealtimeData{ObservationAt: observedAt})
		return nil
	}
	snapshot, observations, err := parseGTFSRTFeed(ctx, server, client, observedAt)
	observed := make(map[string]bool, len(observations))
	for _, observation := range observations {
		observed[observation.FeedID] = true
		realtimeStore.Observe(server, observation)
	}
	if err != nil {
		if ctx.Err() == nil {
			for feedIdx, feed := range server.GtfsRTFeeds {
				feedID := fmt.Sprintf("%d", feedIdx)
				if !observed[feedID] {
					realtimeStore.Observe(server, FeedObservation{
						FeedID: feedID, FeedURL: utils.SanitizeServerURL(feed.VehiclePositionURL),
						Success: false, ObservedAt: observedAt,
					})
				}
			}
		}
		// On cancellation the unfinished feeds must not be recorded as
		// failed observations: a canceled fetch is incomplete collection,
		// not a feed outage. Feeds that genuinely succeeded above keep
		// their observations either way.
		return err
	}
	if snapshot == nil {
		return nil
	}
	// Agency-mode filtering is deliberately performed before publication so all
	// metric passes consume the same scoped snapshot.
	//
	// Until a static snapshot exists (startup, or a server whose static
	// download keeps failing) there is nothing to attribute against, so the
	// feed is published unfiltered rather than failing the fetch: a failed
	// fetch would drop every vehicle metric and raise a realtime error for
	// what is a static-side problem, already reported by the static download.
	if !server.IsServerScoped() && routeAgencyIndex != nil && routeAgencyIndex.Has(server.ServerKey()) {
		attribution := routeAgencyIndex.Snapshot(server.ServerKey())
		// In a feed whose static data names no other agency there is nothing
		// to filter out, so vehicles that cannot be attributed (no trip, or
		// IDs missing from a stale bundle) are kept: they are exactly what
		// the invalid-vehicle checks exist to surface.
		keepUnresolved := attribution.AttributesOnlyTo(server.ServerKey(), server.AgencyID)
		filtered := make([]models.RealtimeVehicle, 0, len(snapshot.Vehicles))
		for _, realtimeVehicle := range snapshot.Vehicles {
			routeID, tripID := "", ""
			if realtimeVehicle.Vehicle.Trip != nil {
				routeID = realtimeVehicle.Vehicle.Trip.ID.RouteID
				tripID = realtimeVehicle.Vehicle.Trip.ID.ID
			}
			resolution := attribution.ResolveVehicleAttribution(server.ServerKey(), routeID, tripID)
			if (resolution.Resolved() && resolution.AgencyID == server.AgencyID) || (!resolution.Resolved() && keepUnresolved) {
				filtered = append(filtered, realtimeVehicle)
			}
		}
		snapshot.Vehicles = filtered
	}
	realtimeStore.Set(server.ServerKey(), snapshot)
	return nil
}

// parseGTFSRTFeed fetches one upstream-merged source and preserves its IDs.
func parseGTFSRTFeed(ctx context.Context, server models.ObaServer, client *http.Client, observedAt time.Time) (*models.RealtimeData, []FeedObservation, error) {
	if len(server.GtfsRTFeeds) != 1 {
		return nil, nil, fmt.Errorf("expected one upstream-merged realtime source; merge feeds upstream")
	}
	snapshot := &models.RealtimeData{ObservationAt: observedAt}
	observations := make([]FeedObservation, 0, len(server.GtfsRTFeeds))
	// reportFetchErr silences failures caused by cycle-deadline or shutdown
	// cancellation: an interrupted fetch is incomplete collection, not a
	// feed failure, and must neither page Sentry nor (via the caller) mark
	// the feed down.
	reportFetchErr := func(err error, opts report.SentryReportOptions) {
		if ctx.Err() != nil {
			return
		}
		report.ReportErrorWithSentryOptions(err, opts)
	}
	feed := server.GtfsRTFeeds[0]
	feedID := "0"
	sanitizedURL := utils.SanitizeServerURL(feed.VehiclePositionURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.VehiclePositionURL, nil)
	if err != nil {
		err = fmt.Errorf("create GTFS-RT request: %w", err)
		reportFetchErr(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": sanitizedURL,
			},
		})
		return nil, observations, err
	}
	if feed.GtfsRTAPIKey != "" {
		req.Header.Set(feed.GtfsRTAPIKey, feed.GtfsRTAPIValue)
	}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("fetch GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
		reportFetchErr(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": sanitizedURL,
			},
		})
		return nil, observations, err
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		err = fmt.Errorf("read GTFS-RT feed %s: %w", feed.VehiclePositionURL, readErr)
		reportFetchErr(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": sanitizedURL,
			},
		})
		return nil, observations, err
	}
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GTFS-RT feed %s returned %s", feed.VehiclePositionURL, resp.Status)
		reportFetchErr(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
			ExtraContext: map[string]interface{}{
				"vehicle_position_url": sanitizedURL,
				"status":               resp.Status,
			},
		})
		return nil, observations, err
	}
	var raw gtfsrt.FeedMessage
	if err := proto.Unmarshal(data, &raw); err != nil {
		err = fmt.Errorf("decode raw GTFS-RT feed %s: %w", sanitizedURL, err)
		reportFetchErr(err, report.SentryReportOptions{Tags: map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID}})
		return nil, observations, err
	}
	if err := ctx.Err(); err != nil {
		return nil, observations, err
	}
	entities, duplicateErr := excludeDuplicateRealtimeIDs(&raw)
	if duplicateErr != nil {
		reportFetchErr(duplicateErr, report.SentryReportOptions{
			Tags:         map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID},
			ExtraContext: map[string]interface{}{"vehicle_position_url": sanitizedURL},
		})
		// Parse only unambiguous entities. Keep raw for source-payload health
		// metrics; exclusion must not hide a defect in the producer's feed.
		filtered := &gtfsrt.FeedMessage{Header: raw.Header, Entity: entities}
		var err error
		data, err = proto.Marshal(filtered)
		if err != nil {
			return nil, observations, fmt.Errorf("encode filtered GTFS-RT feed %s: %w", sanitizedURL, err)
		}
	}

	parsed, err := remoteGtfs.ParseRealtime(data, &remoteGtfs.ParseRealtimeOptions{})
	if err != nil {
		err = fmt.Errorf("parse GTFS-RT feed %s: %w", sanitizedURL, err)
		reportFetchErr(err, report.SentryReportOptions{
			Tags:         map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
			ExtraContext: map[string]interface{}{"vehicle_position_url": sanitizedURL},
		})
		return nil, observations, err
	}

	payloadHash := canonicalEntityHash(raw.Entity)
	stateHashes := vehicleStateHashes(entities)
	vehicleEntities := 0
	missingTimestamps := 0
	for _, entity := range raw.Entity {
		if entity.GetVehicle() == nil {
			continue
		}
		vehicleEntities++
		if entity.GetVehicle().Timestamp == nil {
			missingTimestamps++
		}
	}
	var sourceTimestamp *time.Time
	if raw.Header != nil && raw.Header.Timestamp != nil {
		timestamp := time.Unix(int64(raw.Header.GetTimestamp()), 0).UTC()
		sourceTimestamp = &timestamp
	}
	observations = append(observations, FeedObservation{
		FeedID: feedID, FeedURL: sanitizedURL, Success: true, ObservedAt: observedAt,
		DuplicateID:     duplicateErr != nil,
		SourceTimestamp: sourceTimestamp, PayloadHash: payloadHash,
		VehicleEntities: vehicleEntities, VehicleTimestampMissing: missingTimestamps,
	})
	snapshot.Feeds = append(snapshot.Feeds, models.RealtimeFeed{
		FeedID: feedID, FeedURL: sanitizedURL,
		FullDataset: raw.Header == nil || raw.Header.GetIncrementality() == gtfsrt.FeedHeader_FULL_DATASET,
	})
	positionIDs := make(map[string]struct{})
	for _, vehicle := range parsed.Vehicles {
		if vehicle.IsEntityInMessage && vehicle.ID != nil && vehicle.ID.ID != "" {
			positionIDs[vehicle.ID.ID] = struct{}{}
		}
	}
	for _, vehicle := range parsed.Vehicles {
		id := ""
		if vehicle.ID != nil {
			id = vehicle.ID.ID
		}
		// go-gtfs keys descriptors by ID, label, and license plate. A trip
		// update can therefore produce a second, reference-only entry for a
		// position's vehicle. It must not consume that position's state hash.
		if !vehicle.IsEntityInMessage {
			if _, hasPosition := positionIDs[id]; hasPosition {
				continue
			}
		}
		stateHash := ""
		if hashes := stateHashes[id]; len(hashes) > 0 {
			stateHash = hashes[0]
			stateHashes[id] = hashes[1:]
		}
		snapshot.Vehicles = append(snapshot.Vehicles, models.RealtimeVehicle{Vehicle: vehicle, FeedID: feedID, StateHash: stateHash})
	}
	return snapshot, observations, nil
}

func canonicalEntityHash(entities []*gtfsrt.FeedEntity) string {
	hashes := make([]string, 0, len(entities))
	for _, entity := range entities {
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(entity)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	sort.Strings(hashes)
	sum := sha256.Sum256([]byte(strings.Join(hashes, "\n")))
	return hex.EncodeToString(sum[:])
}

func vehicleStateHashes(entities []*gtfsrt.FeedEntity) map[string][]string {
	hashes := make(map[string][]string)
	for _, entity := range entities {
		if entity.GetVehicle() == nil {
			continue
		}
		vehicle := proto.Clone(entity.GetVehicle()).(*gtfsrt.VehiclePosition)
		vehicle.Timestamp = nil
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(vehicle)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		id := vehicle.GetVehicle().GetId()
		hashes[id] = append(hashes[id], hex.EncodeToString(sum[:]))
	}
	return hashes
}
