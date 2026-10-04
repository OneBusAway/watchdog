package gtfs

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
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
//  1. Downloads every configured GTFS static feed (with backoff retries).
//  2. In agency-mode, builds and stores an owned agency-scoped static snapshot.
//  3. In server-mode, merges the feeds into one StaticData per server and stores
//     the shared pointer under every declared agency key.
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
			bundles := make([]*remoteGtfs.Static, 0, len(s.GtfsStaticFeeds))
			downloaded := make([]downloadedStaticFeed, 0, len(s.GtfsStaticFeeds))
			var downloadErrors []error
			for _, gtfsURL := range s.GtfsStaticFeeds {
				staticBundle, rawData, err := downloadGTFSBundleData(ctx, client, gtfsURL, s.AgencyID, maxRetries)
				if err == nil {
					bundles = append(bundles, staticBundle)
					downloaded = append(downloaded, downloadedStaticFeed{url: gtfsURL, data: rawData, bundle: staticBundle})
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
				downloadErrors = append(downloadErrors, err)
				continue
			}
			if len(bundles) != len(s.GtfsStaticFeeds) {
				markScheduleUnavailable()
				err := fmt.Errorf("only %d of %d configured GTFS static feeds downloaded: %v", len(bundles), len(s.GtfsStaticFeeds), downloadErrors)
				logger.Error("Incomplete GTFS static refresh; preserving prior complete snapshot", "server_name", s.ServerName, "agency_id", s.AgencyID, "error", err)
				finish(err)
				return
			}
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
				report.ReportErrorWithSentryOptions(storeErr, report.SentryReportOptions{
					Tags: map[string]string{
						"agency_id":   s.AgencyID,
						"server_name": s.ServerName,
					},
					Level: sentry.LevelError,
				})
				logger.Error("Failed to store GTFS bundles", "agency_id", s.AgencyID, "server_name", s.ServerName, "error", storeErr)
				finish(storeErr)
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
// Server-mode bounding boxes are computed from the original source bundles
// before their stops are merged and duplicate IDs are removed. Agency-mode
// computes its box from the retained scoped stops.
//
// observer, if non-nil, is invoked once per retained (server, agency) tuple.
// A nil bundle retires an agency removed by a complete server-scoped refresh.
func storeStaticForServer(server models.ObaServer, bundles []*remoteGtfs.Static, staticStore *StaticStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *RouteAgencyIndex, observer StaticBundleObserver, logger *slog.Logger) error {
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

	mergedbundle, declaredAgencies := mergeStaticAndDiscoverAgencies(bundles)
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
	// fallback when an agency has no usable stops. Bounds are computed directly
	// from the source feeds, before duplicate stop IDs are removed from the
	// merged bundle, so every physical location contributes without being stored
	// in a persistent per-agency stop index.
	unionBox, unionBoxErr := computedBoxes.union, computedBoxes.unionErr
	agencyBoxes, agencyBoxErrors := computedBoxes.byAgency, computedBoxes.errorsByAgency
	// Per-agency storage. The merged StaticData is pointer-shared across all
	// serverKeys. Only the four transient bounding-box extrema above are kept per
	// agency, so stop storage does not grow with the number of declared agencies.
	snapshots := make(map[string]*models.StaticData, len(storageAgencies))
	for _, declaredAgency := range storageAgencies {
		snapshots[models.ServerKey(server.ObaBaseURL, declaredAgency.AgencyID)] = mergedbundle
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
				observer(server, declaredAgency.AgencyID, declaredAgency.AgencyName, mergedbundle)
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

// boundingBoxAccumulator incrementally computes geographic bounds without
// retaining the stops that produced them. In server mode computeBoundingBoxes
// creates one for the server-wide union and one for each declared agency.
// Its memory use remains constant as stops are added: box holds the four
// extrema, stopCount distinguishes an empty feed from one whose coordinates are
// all invalid, and initialized records whether a valid coordinate was seen.
type boundingBoxAccumulator struct {
	box         geo.BoundingBox
	stopCount   int
	initialized bool
}

// add incorporates one stop into the running bounds. All stops increment
// stopCount, but stops with missing or NaN coordinates cannot affect the box.
// The first valid coordinate initializes all four extrema; every later valid
// coordinate updates only the minima or maxima it exceeds.
func (a *boundingBoxAccumulator) add(stop remoteGtfs.Stop) {
	a.stopCount++
	if stop.Latitude == nil || stop.Longitude == nil {
		return
	}
	lat, lon := *stop.Latitude, *stop.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return
	}
	if !a.initialized {
		a.box = geo.BoundingBox{MinLat: lat, MaxLat: lat, MinLon: lon, MaxLon: lon}
		a.initialized = true
		return
	}
	if lat < a.box.MinLat {
		a.box.MinLat = lat
	}
	if lat > a.box.MaxLat {
		a.box.MaxLat = lat
	}
	if lon < a.box.MinLon {
		a.box.MinLon = lon
	}
	if lon > a.box.MaxLon {
		a.box.MaxLon = lon
	}
}

// result finalizes an accumulator after its source stops have been processed.
// It distinguishes an accumulator that received no stops from one that received
// stops but never saw a valid latitude/longitude pair, allowing the caller to
// log the appropriate failure or fall back to the server-wide union box.
func (a *boundingBoxAccumulator) result() (geo.BoundingBox, error) {
	if a.stopCount == 0 {
		return geo.BoundingBox{}, fmt.Errorf("no stops to compute bounding box")
	}
	if !a.initialized {
		return geo.BoundingBox{}, fmt.Errorf("no valid latitude/longitude found in stops")
	}
	return a.box, nil
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

// computeBoundingBoxes calculates all agency and server-wide bounds while the
// source feeds are still separate. For each feed it first collects the agency
// IDs declared by that feed. It then adds every stop once to the union
// accumulator and once to each of those agency accumulators. A feed with no
// non-empty agency_id contributes only to the union.
//
// This feed provenance provides the desired scoping without a persistent
// stop-to-agency index. Separate single-agency feeds naturally produce distinct
// boxes; one pre-merged multi-agency feed gives all of its agencies identical
// boxes. Duplicate stop IDs at different coordinates still contribute to the
// bounds because this pass runs over the source feeds before
// mergeStaticAndDiscoverAgencies applies first-occurrence-wins deduplication.
//
// Once the walk finishes, each accumulator is finalized into either a
// geo.BoundingBox or an error. storeStaticForServer stores successful agency
// boxes and uses union as the fallback for agencies without usable bounds.
func computeBoundingBoxes(bundles []*remoteGtfs.Static) computedBoundingBoxes {
	union := &boundingBoxAccumulator{}
	byAgency := make(map[string]*boundingBoxAccumulator)
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
				byAgency[agency.Id] = &boundingBoxAccumulator{}
			}
		}

		for _, stop := range bundle.Stops {
			union.add(stop)
			for agencyID := range agencyIDs {
				byAgency[agencyID].add(stop)
			}
		}
	}

	result := computedBoundingBoxes{
		byAgency:       make(map[string]geo.BoundingBox, len(byAgency)),
		errorsByAgency: make(map[string]error),
	}
	result.union, result.unionErr = union.result()
	for agencyID, accumulator := range byAgency {
		box, err := accumulator.result()
		if accumulator.stopCount == 0 {
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

// declaredAgency is the agency identity recovered from agency.txt during merge.
type declaredAgency struct {
	AgencyID   string
	AgencyName string
}

// mergeStaticAndDiscoverAgencies merges parsed bundles into one StaticData
// while extracting the set of declared agencies from agency.txt. Returns the
// merged bundle (empty if no input) and the declared-agency list.
//
// Multi-agency feeds are accepted: if a feed's agency.txt declares agencies
// A and B, both end up in the returned list. Duplicate (agency_id, agency_name,
// agency_url) rows across feeds collapse silently — the first occurrence
// wins. Collisions where the identity fields disagree are reported to Sentry
// at warning level (see Change 2 below) but the kept entry is still the
// first occurrence.
//
// Stop-id collisions (same stop_id at different lat/lon) are reported to
// Sentry. The flattened Stops slice remains first-occurrence-wins; bounding
// boxes are computed from the source feeds before this deduplication.
func mergeStaticAndDiscoverAgencies(bundles []*remoteGtfs.Static) (*models.StaticData, []declaredAgency) {
	if len(bundles) == 0 {
		return &models.StaticData{}, nil
	}
	staticData := &models.StaticData{}
	// stopID → stopLocation (first occurrence kept in flattened Stops)
	keptLocationByID := make(map[string]stopLocation)
	// agencyID → agencyIdentity (first occurrence kept)
	agenciesByID := make(map[string]agencyIdentity)
	for _, staticBundle := range bundles {
		if staticBundle == nil {
			continue
		}
		data := models.NewStaticData(staticBundle)
		for _, stop := range data.Stops {
			kept, keptExists := keptLocationByID[stop.Id]
			if !keptExists {
				staticData.Stops = append(staticData.Stops, stop)
				keptLocationByID[stop.Id] = stopLocation{lat: stop.Latitude, lon: stop.Longitude}
				continue
			}
			if sameStopLocation(kept.lat, kept.lon, stop.Latitude, stop.Longitude) {
				// Exact duplicate (same id, same location). Silent skip.
				continue
			}
			// stop_id collision with different location: warn and keep the first
			// occurrence in the merged bundle.
			report.ReportErrorWithSentryOptions(
				fmt.Errorf("static bundle has a duplicate stop_id %q at a different location; existing=(lat=%s, lon=%s), duplicate=(lat=%s, lon=%s); keeping first occurrence",
					stop.Id,
					formatLatLon(kept.lat), formatLatLon(kept.lon),
					formatLatLon(stop.Latitude), formatLatLon(stop.Longitude)),
				report.SentryReportOptions{
					Tags: map[string]string{"stop_id": stop.Id},
					ExtraContext: map[string]interface{}{
						"existing_lat":  kept.lat,
						"existing_lon":  kept.lon,
						"duplicate_lat": stop.Latitude,
						"duplicate_lon": stop.Longitude,
					},
					Level: sentry.LevelWarning,
				},
			)
		}
		for _, agency := range data.Agencies {
			if agency.Id == "" {
				continue
			}
			knownAgency, exists := agenciesByID[agency.Id]
			if !exists {
				agenciesByID[agency.Id] = agencyIdentity{Name: agency.Name, Url: agency.Url}
				staticData.Agencies = append(staticData.Agencies, agency)
				continue
			}
			if knownAgency.Name == agency.Name && knownAgency.Url == agency.Url {
				// Exact duplicate (same id, name, url). Silent skip.
				continue
			}
			// agency_id collision with mismatching identity — warn.
			report.ReportErrorWithSentryOptions(
				fmt.Errorf("static bundle has a duplicate agency_id %q with mismatching identity; existing=(name=%q, url=%q), duplicate=(name=%q, url=%q); keeping first occurrence",
					agency.Id, knownAgency.Name, knownAgency.Url, agency.Name, agency.Url),
				report.SentryReportOptions{
					Tags: map[string]string{"agency_id": agency.Id},
					ExtraContext: map[string]interface{}{
						"existing_name":  knownAgency.Name,
						"existing_url":   knownAgency.Url,
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
	staticBundle, err := remoteGtfs.ParseStatic(data, remoteGtfs.ParseStaticOptions{})
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

// getStopLocationsByIDs retrieves stops by ID from the shared merged GTFS
// bundle. The merge keeps the first occurrence of a duplicate stop ID, so each
// requested ID resolves to at most one stop.
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

// fetchAndStoreGTFSRTFeed fetches the GTFS-Realtime feed(s) for a single OBA
// server, parses the vehicle positions, and stores the merged result in the
// RealtimeStore under server.ServerKey().
//
// One key, one fetch, per tick. For a server-scoped entry the key is the
// server-scoped one (empty agency_id), because the vehicle pass reads the
// merged feed once for the whole server and attributes each vehicle to its
// owning agency through the route/trip attribution index. Registering the same feed
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
func fetchAndStoreGTFSRTFeed(ctx context.Context, server models.ObaServer, realtimeStore *RealtimeStore, client *http.Client, routeAgencyIndex *RouteAgencyIndex) error {
	realtimeStore.ReconcileConfiguration(server)
	observedAt := time.Now().UTC()
	merged, observations, err := parseGTFSRTFeeds(ctx, server, client, observedAt)
	succeeded := make(map[string]bool, len(observations))
	for _, observation := range observations {
		succeeded[observation.FeedID] = true
		realtimeStore.Observe(server, observation)
	}
	if err != nil {
		for feedIdx, feed := range server.GtfsRTFeeds {
			feedID := fmt.Sprintf("%d", feedIdx)
			if !succeeded[feedID] {
				realtimeStore.Observe(server, FeedObservation{
					FeedID: feedID, FeedURL: utils.SanitizeServerURL(feed.VehiclePositionURL),
					Success: false, ObservedAt: observedAt,
				})
			}
		}
		return err
	}
	if merged == nil {
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
		filtered := make([]models.RealtimeVehicle, 0, len(merged.Vehicles))
		for _, realtimeVehicle := range merged.Vehicles {
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
		merged.Vehicles = filtered
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
func parseGTFSRTFeeds(ctx context.Context, server models.ObaServer, client *http.Client, observedAt time.Time) (*models.RealtimeData, []FeedObservation, error) {
	merged := &models.RealtimeData{ObservationAt: observedAt}
	observations := make([]FeedObservation, 0, len(server.GtfsRTFeeds))
	for feedIdx, feed := range server.GtfsRTFeeds {
		feedID := fmt.Sprintf("%d", feedIdx)
		sanitizedURL := utils.SanitizeServerURL(feed.VehiclePositionURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.VehiclePositionURL, nil)
		if err != nil {
			err = fmt.Errorf("create GTFS-RT request: %w", err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
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
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": server.AgencyID, "server_name": server.ServerName},
				ExtraContext: map[string]interface{}{
					"vehicle_position_url": sanitizedURL,
				},
			})
			return nil, observations, err
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
			return nil, observations, err
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
			return nil, observations, err
		}
		var raw gtfsrt.FeedMessage
		if err := proto.Unmarshal(data, &raw); err != nil {
			return nil, observations, fmt.Errorf("decode raw GTFS-RT feed %s: %w", feed.VehiclePositionURL, err)
		}

		payloadHash := canonicalEntityHash(raw.Entity)
		stateHashes := vehicleStateHashes(raw.Entity)
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
			SourceTimestamp: sourceTimestamp, PayloadHash: payloadHash,
			VehicleEntities: vehicleEntities, VehicleTimestampMissing: missingTimestamps,
		})
		merged.Feeds = append(merged.Feeds, models.RealtimeFeed{
			FeedID: feedID, FeedURL: sanitizedURL,
			FullDataset: raw.Header == nil || raw.Header.GetIncrementality() == gtfsrt.FeedHeader_FULL_DATASET,
		})
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
			stateHash := ""
			if hashes := stateHashes[id]; len(hashes) > 0 {
				stateHash = hashes[0]
				stateHashes[id] = hashes[1:]
			}
			merged.Vehicles = append(merged.Vehicles, models.RealtimeVehicle{Vehicle: vehicle, FeedID: feedID, StateHash: stateHash})
		}
	}
	return merged, observations, nil
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
