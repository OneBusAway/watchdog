package history

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// Steps name these writes in logs, Sentry and the write failure counter.
const (
	obaMetricsStep            = "oba_metrics"
	unmatchedStopLoadStep     = "unmatched_stop_load"
	unmatchedStopFlushStep    = "unmatched_stop_flush"
	unmatchedStopMetadataStep = "unmatched_stop_metadata"
	clusterSchemeStep         = "cluster_scheme"
	lastReportedSaveInterval  = 10 * time.Minute
)

// The cluster scheme every stop_cluster_assignment row is written under.
const (
	clusterSchemeVersion     = "s2_l13_root_station_v1"
	clusterS2Level           = 13
	clusterStationGrouping   = "root_parent_station"
	clusterSchemeDescription = "S2 level-13 cell of the stop coordinates and the root of the stop parent_station chain"
)

// unmatchedStopKey identifies a reported stop by the IDs OBA uses.
type unmatchedStopKey struct {
	agencyID string
	stopID   string
}

// openStopEpisode is an open unmatched_stop_episode row held in memory.
type openStopEpisode struct {
	episodeID      int64
	reportedStopID int64
	// agencyID is the agency row's ID, not OBA's agency ID.
	agencyID int64
	// lastReportedAt is the time of the last poll that reported the stop.
	// It is saved at most every lastReportedSaveInterval, and exactly when
	// the episode closes.
	lastReportedAt time.Time
}

// serverStopEpisodes is one server's open episodes. mu serializes every
// change to them: polls, flushes and static version triggers.
type serverStopEpisodes struct {
	mu sync.Mutex
	// server is the entry last seen for this base URL; it tags flush writes.
	server models.ObaServer
	loaded bool
	open   map[unmatchedStopKey]*openStopEpisode
	// savedAt is when every open episode's last_reported_at was last
	// written, measured with observedAt.
	savedAt time.Time
}

// openStopEpisodes holds the open episodes of every server, keyed by the
// sanitized base URL.
type openStopEpisodes struct {
	mu      sync.Mutex
	servers map[string]*serverStopEpisodes
}

func newOpenStopEpisodes() *openStopEpisodes {
	return &openStopEpisodes{servers: make(map[string]*serverStopEpisodes)}
}

// forServer returns the server's entry, creating an unloaded one if needed.
func (o *openStopEpisodes) forServer(baseURL string, server models.ObaServer) *serverStopEpisodes {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.servers[baseURL]
	if !ok {
		entry = &serverStopEpisodes{server: server, open: make(map[unmatchedStopKey]*openStopEpisode)}
		o.servers[baseURL] = entry
	}
	return entry
}

// remove drops the server's entry and returns it, if there was one.
func (o *openStopEpisodes) remove(baseURL string) (*serverStopEpisodes, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.servers[baseURL]
	delete(o.servers, baseURL)
	return entry, ok
}

func (o *openStopEpisodes) all() []*serverStopEpisodes {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries := make([]*serverStopEpisodes, 0, len(o.servers))
	for _, entry := range o.servers {
		entries = append(entries, entry)
	}
	return entries
}

func (o *openStopEpisodes) baseURLs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	urls := make([]string, 0, len(o.servers))
	for url := range o.servers {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	return urls
}

// TrackedServerURLs returns the sanitized base URLs whose open episodes are
// held in memory. app.PruneStaleServers flushes those no longer configured.
func (h *HistoryService) TrackedServerURLs() []string {
	if h == nil || h.stops == nil {
		return nil
	}
	return h.stops.baseURLs()
}

// stopEpisodePlan is what one response changes for one server.
type stopEpisodePlan struct {
	// agencies are the agencies the response is recorded for, without
	// duplicates.
	agencies []models.ObaServer
	// complete holds the agencies whose IDs appear in agencyIDs. Only they
	// open or close episodes.
	complete map[string]bool
	opens    []unmatchedStopKey
	closes   []unmatchedStopKey
	still    []unmatchedStopKey
}

// planStopEpisodes compares a response with the open episodes in memory.
func planStopEpisodes(open map[unmatchedStopKey]*openStopEpisode, agencies []models.ObaServer, response *metrics.OBAMetrics) stopEpisodePlan {
	entry := response.Data.Entry
	reportedAgencies := make(map[string]bool, len(entry.AgencyIDs))
	for _, id := range entry.AgencyIDs {
		reportedAgencies[id] = true
	}

	plan := stopEpisodePlan{complete: make(map[string]bool)}
	seenAgencies := make(map[string]bool, len(agencies))
	reported := make(map[unmatchedStopKey]bool)
	for _, agency := range agencies {
		if agency.AgencyID == "" || seenAgencies[agency.AgencyID] {
			continue
		}
		seenAgencies[agency.AgencyID] = true
		plan.agencies = append(plan.agencies, agency)
		if !reportedAgencies[agency.AgencyID] {
			continue
		}
		plan.complete[agency.AgencyID] = true
		for _, stopID := range entry.StopIDsUnmatched[agency.AgencyID] {
			key := unmatchedStopKey{agencyID: agency.AgencyID, stopID: stopID}
			// An empty ID is not an identity (errEmptyID).
			if stopID == "" || reported[key] {
				continue
			}
			reported[key] = true
			if _, ok := open[key]; ok {
				plan.still = append(plan.still, key)
			} else {
				plan.opens = append(plan.opens, key)
			}
		}
	}
	for key := range open {
		if plan.complete[key.agencyID] && !reported[key] {
			plan.closes = append(plan.closes, key)
		}
	}
	sort.Slice(plan.closes, func(i, j int) bool {
		a, b := plan.closes[i], plan.closes[j]
		if a.agencyID != b.agencyID {
			return a.agencyID < b.agencyID
		}
		return a.stopID < b.stopID
	})
	return plan
}

// RecordOBAMetrics records one OBA metrics response for one server. server is
// the configured entry; agencies are the agencies the response is recorded
// for (the configured agency in agency mode, the agencies whose checks ran in
// server mode). observedAt is when Watchdog received the response.
//
// Call it only after a successful fetch: a failed fetch says nothing about
// which IDs were reported. A response that opens or closes nothing, when the
// 10-minute save is not due, does not touch the database.
func (h *HistoryService) RecordOBAMetrics(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time) {
	if h == nil || h.pool == nil || response == nil || len(agencies) == 0 {
		return
	}
	baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if baseURL == "" {
		return
	}
	// write bounds the transaction, but the queries run on the context fn
	// captures, so bound that too.
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	entry := h.stops.forServer(baseURL, server)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.server = server

	if !entry.loaded && !h.loadOpenEpisodes(ctx, server, baseURL, entry, observedAt) {
		return
	}

	plan := planStopEpisodes(entry.open, agencies, response)
	for _, key := range plan.still {
		entry.open[key].lastReportedAt = observedAt
	}

	closing := make(map[unmatchedStopKey]bool, len(plan.closes))
	for _, key := range plan.closes {
		closing[key] = true
	}
	// others are the episodes that stay open and were open before this
	// poll; the 10-minute save covers them.
	others := make([]*openStopEpisode, 0, len(entry.open))
	for key, episode := range entry.open {
		if !closing[key] {
			others = append(others, episode)
		}
	}
	saveDue := len(entry.open) > 0 && observedAt.Sub(entry.savedAt) >= lastReportedSaveInterval

	if len(plan.opens) == 0 && len(plan.closes) == 0 && !saveDue {
		return
	}

	var opened map[unmatchedStopKey]*openStopEpisode
	committed := h.write(ctx, obaMetricsStep, publicationTags(server), func(q *dbgen.Queries) error {
		opened = nil
		if len(plan.opens) > 0 || len(plan.closes) > 0 {
			var err error
			opened, err = h.recordEpisodeChanges(ctx, q, server, plan, entry.open, response, observedAt)
			if err != nil {
				return err
			}
		}
		if saveDue {
			return saveLastReported(ctx, q, others)
		}
		return nil
	})
	if !committed {
		// Memory still holds the difference, so the next poll records it,
		// with that poll's time.
		return
	}
	for key, episode := range opened {
		entry.open[key] = episode
	}
	for _, key := range plan.closes {
		delete(entry.open, key)
	}
	// With no episode open before this poll, everything open now was
	// written exactly, so the save clock restarts too.
	if saveDue || len(others) == 0 {
		entry.savedAt = observedAt
	}
}

// loadOpenEpisodes reads the server's open episodes into entry. It reports
// whether it succeeded; on failure entry stays unloaded and the next call
// tries again.
func (h *HistoryService) loadOpenEpisodes(ctx context.Context, server models.ObaServer, baseURL string, entry *serverStopEpisodes, now time.Time) bool {
	rows, err := dbgen.New(h.pool).ListOpenUnmatchedStopEpisodesForServer(ctx, baseURL)
	if err != nil {
		reportWriteFailure(h.logger, unmatchedStopLoadStep, publicationTags(server), fmt.Errorf("load open unmatched-stop episodes: %w", err))
		return false
	}
	for _, row := range rows {
		entry.open[unmatchedStopKey{agencyID: row.ExternalAgencyID, stopID: row.ObaStopID}] = &openStopEpisode{
			episodeID:      row.UnmatchedStopEpisodeID,
			reportedStopID: row.ReportedStopID,
			agencyID:       row.AgencyID,
			lastReportedAt: row.LastReportedAt,
		}
	}
	entry.loaded = true
	entry.savedAt = now
	return true
}

// recordEpisodeChanges writes the response, its agency rows, the opened
// episodes with their metadata, and the closes. It returns the opened
// episodes, which the caller adds to memory once the transaction commits.
func (h *HistoryService) recordEpisodeChanges(ctx context.Context, q *dbgen.Queries, server models.ObaServer, plan stopEpisodePlan, open map[unmatchedStopKey]*openStopEpisode, response *metrics.OBAMetrics, observedAt time.Time) (map[unmatchedStopKey]*openStopEpisode, error) {
	serverID, err := ensureServer(ctx, q, server, h.resolver)
	if err != nil {
		return nil, err
	}

	var upstreamTime *time.Time
	if response.CurrentTime > 0 {
		t := time.UnixMilli(response.CurrentTime).UTC()
		upstreamTime = &t
	}
	apiVersion := int32(response.Version)
	responseID, err := q.InsertOBAMetricsResponse(ctx, dbgen.InsertOBAMetricsResponseParams{
		ServerID:            serverID,
		ObservedAt:          observedAt,
		UpstreamCurrentTime: upstreamTime,
		ApiVersion:          &apiVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("server %q: insert metrics response: %w", server.ServerName, err)
	}

	entry := response.Data.Entry
	agencyIDs := make(map[string]int64, len(plan.agencies))
	responseAgencyIDs := make(map[string]int64, len(plan.agencies))
	for _, agency := range plan.agencies {
		agencyID, err := ensureAgency(ctx, q, serverID, server.ServerName, agency.AgencyID, agency.AgencyName)
		if err != nil {
			return nil, err
		}
		responseAgencyID, err := q.InsertOBAMetricsResponseAgency(ctx, dbgen.InsertOBAMetricsResponseAgencyParams{
			ResponseID:         responseID,
			AgencyID:           agencyID,
			ResponseComplete:   plan.complete[agency.AgencyID],
			UnmatchedStopCount: countFor(entry.StopIDsUnmatchedCount, agency.AgencyID),
			UnmatchedTripCount: countFor(entry.RealtimeTripCountsUnmatched, agency.AgencyID),
			MatchedStopCount:   countFor(entry.StopIDsMatchedCount, agency.AgencyID),
			MatchedTripCount:   countFor(entry.RealtimeTripCountsMatched, agency.AgencyID),
		})
		if err != nil {
			return nil, fmt.Errorf("server %q: insert metrics response agency %q: %w", server.ServerName, agency.AgencyID, err)
		}
		agencyIDs[agency.AgencyID] = agencyID
		responseAgencyIDs[agency.AgencyID] = responseAgencyID
	}

	versionID, hasVersion := h.CurrentStaticVersion(server.ServerKey())
	stops := newStaticStopIndex(h.staticStore, server)
	opened := make(map[unmatchedStopKey]*openStopEpisode, len(plan.opens))
	for _, key := range plan.opens {
		agencyID := agencyIDs[key.agencyID]
		reportedStopID, err := ensureReportedStop(ctx, q, agencyID, key.stopID)
		if err != nil {
			return nil, err
		}
		episodeID, err := q.InsertUnmatchedStopEpisode(ctx, dbgen.InsertUnmatchedStopEpisodeParams{
			ReportedStopID:        reportedStopID,
			AgencyID:              agencyID,
			FirstResponseAgencyID: responseAgencyIDs[key.agencyID],
			LastReportedAt:        observedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("server %q: open unmatched stop %q: %w", server.ServerName, key.stopID, err)
		}
		episode := &openStopEpisode{
			episodeID:      episodeID,
			reportedStopID: reportedStopID,
			agencyID:       agencyID,
			lastReportedAt: observedAt,
		}
		if hasVersion {
			stop, found := stops.lookup(key.agencyID, key.stopID)
			if err := linkOpenedEpisodeMetadata(ctx, q, versionID, episode, stop, found); err != nil {
				return nil, fmt.Errorf("server %q: metadata for unmatched stop %q: %w", server.ServerName, key.stopID, err)
			}
		}
		opened[key] = episode
	}

	for _, key := range plan.closes {
		episode := open[key]
		if err := q.CloseUnmatchedStopEpisode(ctx, dbgen.CloseUnmatchedStopEpisodeParams{
			ClosingResponseAgencyID: responseAgencyIDs[key.agencyID],
			LastReportedAt:          episode.lastReportedAt,
			UnmatchedStopEpisodeID:  episode.episodeID,
		}); err != nil {
			return nil, fmt.Errorf("server %q: close unmatched stop %q: %w", server.ServerName, key.stopID, err)
		}
	}
	return opened, nil
}

// countFor returns a count from a response map, or nil when the map has no
// key for the agency. A negative count, which the schema rejects, is nil too.
func countFor(counts map[string]int, agencyID string) *int32 {
	n, ok := counts[agencyID]
	if !ok || n < 0 {
		return nil
	}
	v := int32(n)
	return &v
}

// saveLastReported writes the in-memory last_reported_at of episodes in one
// statement.
func saveLastReported(ctx context.Context, q *dbgen.Queries, episodes []*openStopEpisode) error {
	if len(episodes) == 0 {
		return nil
	}
	ids := make([]int64, len(episodes))
	times := make([]int64, len(episodes))
	for i, episode := range episodes {
		ids[i] = episode.episodeID
		times[i] = episode.lastReportedAt.UnixMicro()
	}
	if err := q.SaveUnmatchedStopLastReported(ctx, dbgen.SaveUnmatchedStopLastReportedParams{
		EpisodeIds:     ids,
		LastReportedUs: times,
	}); err != nil {
		return fmt.Errorf("save last_reported_at of %d unmatched-stop episodes: %w", len(episodes), err)
	}
	return nil
}

// FlushServer saves the last_reported_at of a removed server's open episodes,
// then drops the server from memory. app.PruneStaleServers calls it before
// the server's other state is removed.
func (h *HistoryService) FlushServer(ctx context.Context, baseURL string) {
	if h == nil || h.pool == nil || h.stops == nil {
		return
	}
	entry, ok := h.stops.remove(utils.SanitizeServerURL(baseURL))
	if !ok {
		return
	}
	h.flushEntry(ctx, entry)
}

// Flush saves the last_reported_at of every open episode. main calls it on
// clean shutdown, after collection has stopped.
func (h *HistoryService) Flush(ctx context.Context) {
	if h == nil || h.pool == nil || h.stops == nil {
		return
	}
	for _, entry := range h.stops.all() {
		h.flushEntry(ctx, entry)
	}
}

func (h *HistoryService) flushEntry(ctx context.Context, entry *serverStopEpisodes) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.loaded || len(entry.open) == 0 {
		return
	}
	episodes := make([]*openStopEpisode, 0, len(entry.open))
	for _, episode := range entry.open {
		episodes = append(episodes, episode)
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if h.write(ctx, unmatchedStopFlushStep, publicationTags(entry.server), func(q *dbgen.Queries) error {
		return saveLastReported(ctx, q, episodes)
	}) {
		entry.savedAt = latestReported(episodes)
	}
}

func latestReported(episodes []*openStopEpisode) time.Time {
	var latest time.Time
	for _, episode := range episodes {
		if episode.lastReportedAt.After(latest) {
			latest = episode.lastReportedAt
		}
	}
	return latest
}

// stopFacts are the static facts of a stop_metadata_version row.
type stopFacts struct {
	found           bool
	name            *string
	latitude        *float64
	longitude       *float64
	parentStationID *string
	locationType    *int16
}

// stopFactsFor returns the facts for a stop looked up in a static snapshot.
// Coordinates outside the valid range are recorded as absent, since the
// schema rejects them and a rejected row would fail every write that opens
// the stop.
func stopFactsFor(stop remoteGtfs.Stop, found bool) stopFacts {
	if !found {
		return stopFacts{}
	}
	name := stop.Name
	facts := stopFacts{found: true, name: &name}
	if stop.Latitude != nil && stop.Longitude != nil && geo.IsValidLatLon(*stop.Latitude, *stop.Longitude) {
		lat, lon := *stop.Latitude, *stop.Longitude
		facts.latitude, facts.longitude = &lat, &lon
	}
	if stop.Parent != nil {
		parentID := stop.Parent.Id
		facts.parentStationID = &parentID
	}
	if t := int(stop.Type); t >= 0 && t <= 5 {
		locationType := int16(t)
		facts.locationType = &locationType
	}
	return facts
}

func stopFactsFromRow(row dbgen.StopMetadataVersion) stopFacts {
	return stopFacts{
		found:           row.StaticStopFound,
		name:            row.StopName,
		latitude:        row.Latitude,
		longitude:       row.Longitude,
		parentStationID: row.ParentStationID,
		locationType:    row.LocationType,
	}
}

func (f stopFacts) equal(other stopFacts) bool {
	return f.found == other.found &&
		equalPtr(f.name, other.name) &&
		equalPtr(f.latitude, other.latitude) &&
		equalPtr(f.longitude, other.longitude) &&
		equalPtr(f.parentStationID, other.parentStationID) &&
		equalPtr(f.locationType, other.locationType)
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// staticStopIndex looks stops up in Watchdog's static snapshots, indexing
// each snapshot once per write.
type staticStopIndex struct {
	store  *gtfs.StaticStore
	server models.ObaServer
	byKey  map[string]map[string]remoteGtfs.Stop
}

func newStaticStopIndex(store *gtfs.StaticStore, server models.ObaServer) *staticStopIndex {
	return &staticStopIndex{store: store, server: server, byKey: make(map[string]map[string]remoteGtfs.Stop)}
}

// lookup finds a stop in the agency's snapshot, falling back to the server
// key's snapshot in server mode.
func (x *staticStopIndex) lookup(agencyID, stopID string) (remoteGtfs.Stop, bool) {
	keys := []string{models.ServerKey(x.server.ObaBaseURL, agencyID)}
	if x.server.IsServerScoped() {
		keys = append(keys, x.server.ServerKey())
	}
	for _, key := range keys {
		if stop, ok := x.stops(key)[stopID]; ok {
			return stop, true
		}
	}
	return remoteGtfs.Stop{}, false
}

func (x *staticStopIndex) stops(serverKey string) map[string]remoteGtfs.Stop {
	if stops, ok := x.byKey[serverKey]; ok {
		return stops
	}
	var stops map[string]remoteGtfs.Stop
	if x.store != nil {
		if data, ok := x.store.Get(serverKey); ok && data != nil {
			stops = make(map[string]remoteGtfs.Stop, len(data.Stops))
			for _, stop := range data.Stops {
				// Keep the first occurrence, as gtfs.GetStopLocationsByIDs does.
				if _, seen := stops[stop.Id]; !seen {
					stops[stop.Id] = stop
				}
			}
		}
	}
	x.byKey[serverKey] = stops
	return stops
}

// linkOpenedEpisodeMetadata is trigger 1: link a newly opened episode to the
// newest metadata row of its stop when the facts are unchanged, otherwise to
// a row for the current static version.
func linkOpenedEpisodeMetadata(ctx context.Context, q *dbgen.Queries, versionID int64, episode *openStopEpisode, stop remoteGtfs.Stop, found bool) error {
	facts := stopFactsFor(stop, found)
	metadataID := int64(0)
	newest, err := q.GetNewestStopMetadataForStop(ctx, episode.reportedStopID)
	switch {
	case err == nil && stopFactsFromRow(newest.StopMetadataVersion).equal(facts):
		metadataID = newest.StopMetadataVersion.StopMetadataVersionID
	case err == nil || errors.Is(err, pgx.ErrNoRows):
		metadataID, err = insertStopMetadata(ctx, q, versionID, episode, facts, stop)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("newest stop metadata: %w", err)
	}
	return linkEpisodeMetadata(ctx, q, episode, metadataID)
}

// recordStopMetadataForVersion is trigger 2: for each open episode the new
// version covers, write a metadata row when the stop's facts differ from the
// episode's newest linked row (or it has none).
func (h *HistoryService) recordStopMetadataForVersion(ctx context.Context, server models.ObaServer, versionID int64) {
	if h == nil || h.pool == nil {
		return
	}
	baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if baseURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	entry := h.stops.forServer(baseURL, server)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.loaded && !h.loadOpenEpisodes(ctx, server, baseURL, entry, time.Now().UTC()) {
		return
	}

	type covered struct {
		key     unmatchedStopKey
		episode *openStopEpisode
	}
	var episodes []covered
	for key, episode := range entry.open {
		// An agency-scoped version covers only its own agency.
		if server.IsServerScoped() || key.agencyID == server.AgencyID {
			episodes = append(episodes, covered{key: key, episode: episode})
		}
	}
	if len(episodes) == 0 {
		return
	}

	stops := newStaticStopIndex(h.staticStore, server)
	h.write(ctx, unmatchedStopMetadataStep, publicationTags(server), func(q *dbgen.Queries) error {
		for _, c := range episodes {
			stop, found := stops.lookup(c.key.agencyID, c.key.stopID)
			facts := stopFactsFor(stop, found)
			newest, err := q.GetNewestLinkedStopMetadata(ctx, c.episode.episodeID)
			if err == nil && stopFactsFromRow(newest.StopMetadataVersion).equal(facts) {
				continue
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("server %q: newest linked metadata for stop %q: %w", server.ServerName, c.key.stopID, err)
			}
			metadataID, err := insertStopMetadata(ctx, q, versionID, c.episode, facts, stop)
			if err != nil {
				return fmt.Errorf("server %q: metadata for stop %q: %w", server.ServerName, c.key.stopID, err)
			}
			if err := linkEpisodeMetadata(ctx, q, c.episode, metadataID); err != nil {
				return fmt.Errorf("server %q: link metadata for stop %q: %w", server.ServerName, c.key.stopID, err)
			}
		}
		return nil
	})
}

// insertStopMetadata returns the stop's metadata row for versionID, inserting
// it when it does not exist yet. A newly inserted found row gets its cluster
// assignment.
func insertStopMetadata(ctx context.Context, q *dbgen.Queries, versionID int64, episode *openStopEpisode, facts stopFacts, stop remoteGtfs.Stop) (int64, error) {
	row, err := q.InsertStopMetadataVersion(ctx, dbgen.InsertStopMetadataVersionParams{
		GtfsStaticVersionID: versionID,
		AgencyID:            episode.agencyID,
		ReportedStopID:      episode.reportedStopID,
		StaticStopFound:     facts.found,
		StopName:            facts.name,
		Latitude:            facts.latitude,
		Longitude:           facts.longitude,
		ParentStationID:     facts.parentStationID,
		LocationType:        facts.locationType,
	})
	if err != nil {
		return 0, fmt.Errorf("insert stop metadata: %w", err)
	}
	if row.Created && facts.found {
		if err := recordClusterAssignment(ctx, q, row.StopMetadataVersionID, stop); err != nil {
			return 0, err
		}
	}
	return row.StopMetadataVersionID, nil
}

func linkEpisodeMetadata(ctx context.Context, q *dbgen.Queries, episode *openStopEpisode, metadataID int64) error {
	if err := q.LinkUnmatchedStopEpisodeMetadata(ctx, dbgen.LinkUnmatchedStopEpisodeMetadataParams{
		UnmatchedStopEpisodeID: episode.episodeID,
		StopMetadataVersionID:  metadataID,
		ReportedStopID:         episode.reportedStopID,
		AgencyID:               episode.agencyID,
	}); err != nil {
		return fmt.Errorf("link stop metadata: %w", err)
	}
	return nil
}

// recordClusterAssignment assigns a stop's metadata row to its S2 cell and
// root station. A stop geo.GetClusterID cannot place gets no assignment.
func recordClusterAssignment(ctx context.Context, q *dbgen.Queries, metadataID int64, stop remoteGtfs.Stop) error {
	cluster, ok := geo.GetClusterID(stop)
	if !ok {
		return nil
	}
	// The scheme is inserted at startup; inserting it here as well keeps
	// assignments working if that startup write failed.
	if err := ensureClusterScheme(ctx, q); err != nil {
		return err
	}
	if err := q.UpsertS2Cell(ctx, dbgen.UpsertS2CellParams{
		SchemeVersion:   clusterSchemeVersion,
		CellID:          cluster.ID,
		CellLevel:       clusterS2Level,
		CenterLatitude:  cluster.Latitude,
		CenterLongitude: cluster.Longitude,
	}); err != nil {
		return fmt.Errorf("upsert s2 cell %q: %w", cluster.ID, err)
	}
	var stationID, stationName *string
	if cluster.StationID != geo.NoStationID {
		id := cluster.StationID
		stationID = &id
		stationName = rootStationName(stop, id)
	}
	if err := q.InsertStopClusterAssignment(ctx, dbgen.InsertStopClusterAssignmentParams{
		StopMetadataVersionID: metadataID,
		SchemeVersion:         clusterSchemeVersion,
		CellID:                cluster.ID,
		RootStationID:         stationID,
		RootStationName:       stationName,
	}); err != nil {
		return fmt.Errorf("insert stop cluster assignment: %w", err)
	}
	return nil
}

// rootStationName returns the stop_name of the station stationID in the
// stop's parent_station chain, or nil when it is not there.
func rootStationName(stop remoteGtfs.Stop, stationID string) *string {
	for s := &stop; s != nil; s = s.Parent {
		if s.Id == stationID {
			name := s.Name
			return &name
		}
	}
	return nil
}

func ensureClusterScheme(ctx context.Context, q *dbgen.Queries) error {
	if err := q.EnsureClusterScheme(ctx, dbgen.EnsureClusterSchemeParams{
		SchemeVersion:   clusterSchemeVersion,
		S2Level:         clusterS2Level,
		StationGrouping: clusterStationGrouping,
		Description:     clusterSchemeDescription,
	}); err != nil {
		return fmt.Errorf("ensure cluster scheme: %w", err)
	}
	return nil
}

// EnsureClusterScheme inserts the cluster scheme if it is missing. main calls
// it once at startup.
func (h *HistoryService) EnsureClusterScheme(ctx context.Context) {
	if h == nil || h.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	h.write(ctx, clusterSchemeStep, writeTags{}, func(q *dbgen.Queries) error {
		return ensureClusterScheme(ctx, q)
	})
}
