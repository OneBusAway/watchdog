package gtfs

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

type staticSnapshotCandidate struct {
	data             *models.StaticData
	declaredAgencies []declaredAgency
	routeIDs         map[string][]string
	tripIDs          map[string][]string
	agencyNames      map[string]string
	boundingBoxes    computedBoundingBoxes
	schedules        map[string]*ScheduleSnapshot
	mappings         StaticFeedMappingObservation
}

// buildStaticSnapshotCandidate combines one reduced contribution per
// configured feed, in configuration order. It performs checks that require
// seeing multiple feeds together, but does not update shared stores.
func buildStaticSnapshotCandidate(server models.ObaServer, byURL map[string]*staticFeedContribution, logger *slog.Logger) (*staticSnapshotCandidate, error) {
	ordered := make([]*staticFeedContribution, 0, len(server.GtfsStaticFeeds))
	for _, feedURL := range server.GtfsStaticFeeds {
		contribution := byURL[feedURL]
		if contribution == nil {
			return nil, &StaticFeedError{
				FeedURL: feedURL, Stage: StaticFailureCompleteValidate, Reason: StaticReasonUnknown,
				Err: fmt.Errorf("static feed %s has no reduced contribution", feedURL),
			}
		}
		ordered = append(ordered, contribution)
	}

	candidate := &staticSnapshotCandidate{
		boundingBoxes: mergeContributionBoundingBoxes(ordered),
		routeIDs:      make(map[string][]string),
		tripIDs:       make(map[string][]string),
		mappings: StaticFeedMappingObservation{
			ConfiguredFeedURLs: make([]string, 0, len(server.GtfsStaticFeeds)),
			Results:            make([]StaticFeedMappingResult, 0, len(ordered)),
		},
	}
	for _, feedURL := range server.GtfsStaticFeeds {
		candidate.mappings.ConfiguredFeedURLs = append(candidate.mappings.ConfiguredFeedURLs, utils.SanitizeServerURL(feedURL))
	}
	for _, contribution := range ordered {
		candidate.mappings.Results = append(candidate.mappings.Results, contribution.agencyMapping)
	}

	if server.IsServerScoped() {
		bundles := make([]*remoteGtfs.Static, 0, len(ordered))
		for _, contribution := range ordered {
			bundles = append(bundles, staticDataForMerge(contribution.data))
		}
		merged, declared := mergeStaticAndDiscoverAgencies(bundles)
		if len(declared) == 0 {
			return nil, &StaticFeedError{
				Stage: StaticFailureCompleteValidate, Reason: StaticReasonUnknown,
				Err: fmt.Errorf("no agencies were declared across the configured static feeds"),
			}
		}
		candidate.data = cloneStaticData(merged)
		candidate.declaredAgencies = declared
		candidate.agencyNames = make(map[string]string, len(declared))
		for _, agency := range declared {
			candidate.agencyNames[agency.AgencyID] = agency.AgencyName
		}
	} else {
		candidate.data = mergeAgencyStaticContributions(server, ordered)
		candidate.agencyNames = map[string]string{server.AgencyID: candidate.data.Agencies[0].Name}
	}

	candidate.routeIDs, candidate.tripIDs = mergeContributionAttributions(server, ordered, logger)
	schedules, err := mergeContributionSchedules(ordered)
	if err != nil {
		return nil, err
	}
	candidate.schedules = schedules
	return candidate, nil
}

func staticDataForMerge(data *models.StaticData) *remoteGtfs.Static {
	if data == nil {
		return &remoteGtfs.Static{}
	}
	return &remoteGtfs.Static{
		Agencies: data.Agencies,
		Routes:   data.Routes,
		Stops:    data.Stops,
		Services: data.Services,
	}
}

func mergeAgencyStaticContributions(server models.ObaServer, contributions []*staticFeedContribution) *models.StaticData {
	var selectedAgency *remoteGtfs.Agency
	for _, contribution := range contributions {
		if contribution.hasSourceAgency && contribution.sourceAgency != nil && contribution.sourceAgency.Id == server.AgencyID {
			agency := *contribution.sourceAgency
			selectedAgency = &agency
			break
		}
	}
	if selectedAgency == nil {
		for _, contribution := range contributions {
			if contribution.hasSourceAgency && contribution.sourceAgency != nil {
				agency := *contribution.sourceAgency
				selectedAgency = &agency
				break
			}
		}
	}
	agency := remoteGtfs.Agency{Id: server.AgencyID, Name: server.AgencyName}
	if selectedAgency != nil {
		agency = *selectedAgency
		if agency.Id == "" {
			agency.Id = server.AgencyID
		}
		if agency.Name == "" {
			agency.Name = server.AgencyName
		}
	}

	result := &models.StaticData{Agencies: []remoteGtfs.Agency{agency}}
	seenRoutes := make(map[string]struct{})
	seenStops := make(map[string]struct{})
	parentIDs := make(map[string]string)
	for _, contribution := range contributions {
		if contribution.data == nil {
			continue
		}
		for _, source := range contribution.data.Routes {
			if source.Id == "" {
				continue
			}
			if _, seen := seenRoutes[source.Id]; seen {
				continue
			}
			seenRoutes[source.Id] = struct{}{}
			route := cloneRoute(source)
			route.Agency = &result.Agencies[0]
			result.Routes = append(result.Routes, route)
		}
		for _, source := range contribution.data.Services {
			result.Services = append(result.Services, cloneService(source))
		}
		for _, source := range contribution.data.Stops {
			if source.Id == "" {
				continue
			}
			if _, seen := seenStops[source.Id]; seen {
				continue
			}
			seenStops[source.Id] = struct{}{}
			result.Stops = append(result.Stops, cloneStop(source))
			if source.Parent != nil {
				parentIDs[source.Id] = source.Parent.Id
			}
		}
	}

	stopsByID := make(map[string]*remoteGtfs.Stop, len(result.Stops))
	for i := range result.Stops {
		stopsByID[result.Stops[i].Id] = &result.Stops[i]
	}
	for i := range result.Stops {
		parent := stopsByID[parentIDs[result.Stops[i].Id]]
		child := &result.Stops[i]
		if parent != nil && parent != child && !stopParentCycle(parent, child) {
			child.Parent = parent
		}
	}
	return result
}

func mergeContributionAttributions(server models.ObaServer, contributions []*staticFeedContribution, logger *slog.Logger) (map[string][]string, map[string][]string) {
	routes := make(map[string][]string)
	trips := make(map[string][]string)
	for _, contribution := range contributions {
		merge := func(destination, source map[string][]string, kind string) {
			keys := make([]string, 0, len(source))
			for key := range source {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				for _, agencyID := range source[key] {
					addAttribution(destination, key, agencyID, kind, server, logger)
				}
			}
		}
		merge(routes, contribution.routeIDs, "route")
		merge(trips, contribution.tripIDs, "trip")
	}
	return routes, trips
}

func mergeContributionBoundingBoxes(contributions []*staticFeedContribution) computedBoundingBoxes {
	result := computedBoundingBoxes{
		byAgency:       make(map[string]geo.BoundingBox),
		errorsByAgency: make(map[string]error),
	}
	unionSet := false
	for _, contribution := range contributions {
		boxes := contribution.boundingBoxes
		if boxes.unionErr == nil {
			if unionSet {
				result.union = mergeBoundingBoxes(result.union, boxes.union)
			} else {
				result.union = boxes.union
				unionSet = true
			}
		} else if result.unionErr == nil {
			result.unionErr = boxes.unionErr
		}
		for agencyID, box := range boxes.byAgency {
			if current, exists := result.byAgency[agencyID]; exists {
				result.byAgency[agencyID] = mergeBoundingBoxes(current, box)
			} else {
				result.byAgency[agencyID] = box
			}
		}
		for agencyID, err := range boxes.errorsByAgency {
			if _, valid := result.byAgency[agencyID]; !valid && result.errorsByAgency[agencyID] == nil {
				result.errorsByAgency[agencyID] = err
			}
		}
	}
	if unionSet {
		result.unionErr = nil
	}
	for agencyID := range result.byAgency {
		delete(result.errorsByAgency, agencyID)
	}
	return result
}

func mergeBoundingBoxes(left, right geo.BoundingBox) geo.BoundingBox {
	return geo.BoundingBox{
		MinLat: min(left.MinLat, right.MinLat),
		MaxLat: max(left.MaxLat, right.MaxLat),
		MinLon: min(left.MinLon, right.MinLon),
		MaxLon: max(left.MaxLon, right.MaxLon),
	}
}

func mergeContributionSchedules(contributions []*staticFeedContribution) (map[string]*ScheduleSnapshot, error) {
	merged := make(map[string]*ScheduleSnapshot)
	for _, contribution := range contributions {
		for serverKey, feedSchedule := range contribution.feedSchedules {
			if feedSchedule == nil || !feedSchedule.complete {
				return nil, &StaticFeedError{
					FeedURL: contribution.url, Stage: StaticFailureCompleteValidate, Reason: StaticReasonInvalidSchedule,
					ContentHash: contribution.contentHash,
					Err:         fmt.Errorf("static feed %s has no complete schedule contribution for %s", contribution.url, serverKey),
				}
			}
			current := merged[serverKey]
			if current == nil {
				current = &ScheduleSnapshot{
					timezone: feedSchedule.timezone, timezoneName: feedSchedule.timezoneName,
					services:           cloneScheduleServices(feedSchedule.services),
					feedURLs:           append([]string(nil), feedSchedule.feedURLs...),
					maxOvernightOffset: feedSchedule.maxOvernightOffset,
				}
				merged[serverKey] = current
				continue
			}
			if !sameTimezoneRules(current.timezone, feedSchedule.timezone) {
				return nil, &StaticFeedError{
					FeedURL: contribution.url, Stage: StaticFailureCompleteValidate, Reason: StaticReasonInvalidSchedule,
					ContentHash: contribution.contentHash,
					Err:         fmt.Errorf("agency schedule combines different timezones %q and %q", current.timezoneName, feedSchedule.timezoneName),
				}
			}
			current.services = append(current.services, cloneScheduleServices(feedSchedule.services)...)
			current.feedURLs = append(current.feedURLs, feedSchedule.feedURLs...)
			if feedSchedule.maxOvernightOffset > current.maxOvernightOffset {
				current.maxOvernightOffset = feedSchedule.maxOvernightOffset
			}
		}
	}
	if len(merged) == 0 {
		return nil, &StaticFeedError{Stage: StaticFailureCompleteValidate, Reason: StaticReasonInvalidSchedule, Err: fmt.Errorf("no agency schedules were assembled")}
	}
	for _, snapshot := range merged {
		sort.Strings(snapshot.feedURLs)
		snapshot.feedURLs = compactStrings(snapshot.feedURLs)
		snapshot.complete = true
	}
	return merged, nil
}

func cloneScheduleServices(source []scheduleService) []scheduleService {
	cloned := make([]scheduleService, len(source))
	for i, service := range source {
		cloned[i] = service
		cloned[i].added = append([]int(nil), service.added...)
		cloned[i].removed = append([]int(nil), service.removed...)
		cloned[i].windows = append([]scheduleWindow(nil), service.windows...)
	}
	return cloned
}

func (gs *GtfsService) assembleAndPublishStaticSnapshot(ctx context.Context, server models.ObaServer, contributions map[string]*staticFeedContribution) error {
	candidate, err := buildStaticSnapshotCandidate(server, contributions, gs.Logger)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	configured := false
	gs.StaticStore.WithRefreshLock(func() {
		if ctx.Err() != nil || !gs.StaticStore.IsConfigured(server) {
			return
		}
		configured = true
		if server.IsServerScoped() {
			gs.publishServerScopedSnapshot(server, candidate)
		} else {
			gs.publishAgencyScopedSnapshot(server, candidate)
		}
		gs.StaticStore.ScheduleStore().Replace(server, candidate.schedules)
		if gs.MappingObserver != nil {
			func() {
				defer func() { _ = recover() }()
				gs.MappingObserver(server, candidate.mappings)
			}()
		}
	})
	if !configured {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errStaticServerUnconfigured
	}
	return nil
}

func (gs *GtfsService) publishAgencyScopedSnapshot(server models.ObaServer, candidate *staticSnapshotCandidate) {
	serverKey := server.ServerKey()
	if len(candidate.data.Routes) == 0 {
		err := fmt.Errorf("no routes in static feeds resolve to configured agency_id %q", server.AgencyID)
		gs.Logger.Warn("Agency-scoped static snapshot is empty", "server_key", serverKey, "agency_id", server.AgencyID, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID}, Level: sentry.LevelWarning,
		})
	}
	gs.StaticStore.ReplaceServerSnapshot(server, map[string]*models.StaticData{serverKey: candidate.data}, time.Now().UTC())
	gs.RouteAgencyIndex.ReplaceCandidates(serverKey, candidate.routeIDs, candidate.tripIDs, candidate.agencyNames)
	if bbox, ok := candidate.boundingBoxes.byAgency[server.AgencyID]; ok {
		gs.BoundingBoxStore.Set(serverKey, bbox)
	} else {
		err := candidate.boundingBoxes.errorsByAgency[server.AgencyID]
		if err == nil {
			err = fmt.Errorf("no stops associated with configured agency_id %q", server.AgencyID)
		}
		gs.BoundingBoxStore.Delete(serverKey)
		gs.Logger.Warn("Could not compute agency bounding box", "server_key", serverKey, "agency_id", server.AgencyID, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"server_name": server.ServerName, "agency_id": server.AgencyID}, Level: sentry.LevelWarning,
		})
	}
	if gs.Observer != nil {
		func() {
			defer func() { _ = recover() }()
			gs.Observer(server, server.AgencyID, candidate.data.Agencies[0].Name, candidate.data)
		}()
	}
}

func (gs *GtfsService) publishServerScopedSnapshot(server models.ObaServer, candidate *staticSnapshotCandidate) {
	snapshots := make(map[string]*models.StaticData, len(candidate.declaredAgencies))
	for _, agency := range candidate.declaredAgencies {
		snapshots[models.ServerKey(server.ObaBaseURL, agency.AgencyID)] = candidate.data
	}
	removedKeys := gs.StaticStore.ReplaceServerSnapshot(server, snapshots, time.Now().UTC())
	for _, removedKey := range removedKeys {
		gs.BoundingBoxStore.Delete(removedKey)
		if gs.Observer != nil {
			agencyID := strings.TrimPrefix(removedKey, models.ServerKeyPrefix(server.ObaBaseURL))
			func() {
				defer func() { _ = recover() }()
				gs.Observer(server, agencyID, "", nil)
			}()
		}
	}

	for _, agency := range candidate.declaredAgencies {
		serverKey := models.ServerKey(server.ObaBaseURL, agency.AgencyID)
		bbox, ok := candidate.boundingBoxes.byAgency[agency.AgencyID]
		if !ok {
			agencyBoxErr, agencyBoxFailed := candidate.boundingBoxes.errorsByAgency[agency.AgencyID]
			if candidate.boundingBoxes.unionErr != nil {
				if agencyBoxFailed {
					gs.Logger.Error("Could not compute agency bounding box", "server_key", serverKey, "agency_id", agency.AgencyID, "error", agencyBoxErr)
					report.ReportErrorWithSentryOptions(
						fmt.Errorf("server %q (%s): could not compute agency bounding box for %s: %w", server.ServerName, server.ObaBaseURL, agency.AgencyID, agencyBoxErr),
						report.SentryReportOptions{Tags: map[string]string{"server_name": server.ServerName, "agency_id": agency.AgencyID}, Level: sentry.LevelError},
					)
				}
				gs.Logger.Error("Could not compute server-wide bounding box", "server_key", serverKey, "error", candidate.boundingBoxes.unionErr)
				report.ReportErrorWithSentryOptions(
					fmt.Errorf("server %q (%s): could not compute server-wide bounding box: %w", server.ServerName, server.ObaBaseURL, candidate.boundingBoxes.unionErr),
					report.SentryReportOptions{Tags: map[string]string{"server_name": server.ServerName}, Level: sentry.LevelError},
				)
			} else {
				if agencyBoxFailed {
					gs.Logger.Warn("Could not compute agency bounding box; using server-wide bounding box", "server_key", serverKey, "agency_id", agency.AgencyID, "error", agencyBoxErr)
					report.ReportErrorWithSentryOptions(
						fmt.Errorf("server %q (%s): could not compute agency bounding box for %s, falling back to union box: %w", server.ServerName, server.ObaBaseURL, agency.AgencyID, agencyBoxErr),
						report.SentryReportOptions{Tags: map[string]string{"server_name": server.ServerName, "agency_id": agency.AgencyID}, Level: sentry.LevelWarning},
					)
				} else {
					gs.Logger.Warn("No stops associated with agency; using server-wide bounding box", "server_key", serverKey, "agency_id", agency.AgencyID)
				}
				bbox = candidate.boundingBoxes.union
				gs.BoundingBoxStore.Set(serverKey, bbox)
			}
		} else {
			gs.BoundingBoxStore.Set(serverKey, bbox)
		}
		if gs.Observer != nil {
			func() {
				defer func() { _ = recover() }()
				gs.Observer(server, agency.AgencyID, agency.AgencyName, candidate.data)
			}()
		}
	}
	if candidate.boundingBoxes.unionErr == nil {
		gs.BoundingBoxStore.Set(models.ServerKey(server.ObaBaseURL, ""), candidate.boundingBoxes.union)
	}
	gs.RouteAgencyIndex.ReplaceCandidates(server.ServerKey(), candidate.routeIDs, candidate.tripIDs, candidate.agencyNames)
}
