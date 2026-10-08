package gtfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// staticFeedContribution is the reduced output of processing one configured
// static feed. It keeps the data needed to assemble a server snapshot without
// retaining the source ZIP or the full remoteGtfs.Static parser graph.
//
// Contributions are not published independently. A refresh campaign can hold
// them while other feeds are retried, then combine them in configured order.
type staticFeedContribution struct {
	url             string
	contentHash     string
	data            *models.StaticData
	routeIDs        map[string][]string
	tripIDs         map[string][]string
	agencyNames     map[string]string
	boundingBoxes   computedBoundingBoxes
	feedSchedules   map[string]*ScheduleSnapshot
	agencyMapping   StaticFeedMappingResult
	sourceAgency    *remoteGtfs.Agency
	hasSourceAgency bool
}

// buildStaticFeedContribution validates one feed and turns it into a self-contained
// contribution for a future complete static snapshot. It prepares the reduced
// static data, schedule, bounds, attribution, and feed-mapping results that
// Watchdog needs, plus the feed URL and content hash that identify the source.
//
// The caller can retain this contribution while other feeds are retried. It is
// not published to shared stores here: the caller must wait until every
// configured feed is ready and the combined candidate passes cross-feed
// validation. The ZIP bytes and parsed bundle are inputs only; the returned
// contribution does not retain either one or the full parser graph.
func buildStaticFeedContribution(server models.ObaServer, feedURL string, zipData []byte, bundle *remoteGtfs.Static, logger *slog.Logger) (*staticFeedContribution, error) {
	sum := sha256.Sum256(zipData)
	contentHash := hex.EncodeToString(sum[:])
	if reason := validateStaticAgencyDiscovery(server, bundle); reason != "" {
		return nil, &StaticFeedError{
			FeedURL: feedURL, Stage: StaticFailureAgencyDiscovery, Reason: reason,
			ContentHash: contentHash,
			Err:         fmt.Errorf("static feed %s failed agency discovery: %s", feedURL, reason),
		}
	}

	rawSchedule, err := parseRawScheduleFeed(feedURL, zipData, server)
	if err != nil {
		return nil, &StaticFeedError{
			FeedURL: feedURL, Stage: StaticFailureSchedule, Reason: StaticReasonInvalidSchedule,
			ContentHash: contentHash,
			Err:         fmt.Errorf("validate schedule in %s: %w", feedURL, err),
		}
	}

	// Compile this feed by itself so the campaign can release its ZIP and raw
	// schedule tables. Cross-feed checks, such as agency timezone consistency,
	// remain part of complete-candidate assembly.
	feedServer := server
	feedServer.GtfsStaticFeeds = []string{feedURL}
	schedules, err := compileRawSchedules(feedServer, []rawScheduleFeed{rawSchedule})
	if err != nil {
		return nil, &StaticFeedError{
			FeedURL: feedURL, Stage: StaticFailureSchedule, Reason: StaticReasonInvalidSchedule,
			ContentHash: contentHash,
			Err:         fmt.Errorf("compile schedule in %s: %w", feedURL, err),
		}
	}

	contribution := &staticFeedContribution{
		url:           feedURL,
		contentHash:   contentHash,
		boundingBoxes: computeBoundingBoxes([]*remoteGtfs.Static{bundle}, server.AgencyID),
		feedSchedules: schedules,
	}

	if server.IsServerScoped() {
		merged, declared := mergeStaticAndDiscoverAgencies([]*remoteGtfs.Static{bundle})
		contribution.data = cloneStaticData(merged)
		contribution.routeIDs, contribution.tripIDs = buildAttributionMaps(server, []*remoteGtfs.Static{bundle}, false, logger)
		contribution.agencyNames = make(map[string]string, len(declared))
		for _, agency := range declared {
			contribution.agencyNames[agency.AgencyID] = agency.AgencyName
		}
	} else {
		agencyResult := buildAgencyStaticSnapshot(server, []*remoteGtfs.Static{bundle}, logger)
		contribution.data = cloneStaticData(agencyResult.data)
		contribution.routeIDs = cloneCandidateMap(agencyResult.routeIDs)
		contribution.tripIDs = cloneCandidateMap(agencyResult.tripIDs)
		contribution.agencyNames = cloneNameMap(agencyResult.agencyNames)
		for i := range bundle.Agencies {
			agency := bundle.Agencies[i]
			if agency.Id == server.AgencyID || (len(bundle.Agencies) == 1 && agency.Id == "") {
				contribution.sourceAgency = &agency
				contribution.hasSourceAgency = true
				break
			}
		}
	}

	observation := classifyStaticFeedMappings(server, []downloadedStaticFeed{{url: feedURL, bundle: bundle}})
	if len(observation.Results) > 0 {
		contribution.agencyMapping = observation.Results[0]
	}
	return contribution, nil
}
