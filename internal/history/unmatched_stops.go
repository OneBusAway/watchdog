package history

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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

// lookup returns the server's entry without creating one.
func (o *openStopEpisodes) lookup(baseURL string) (*serverStopEpisodes, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.servers[baseURL]
	return entry, ok
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
// held in memory. app.OnConfigUpdated flushes those no longer configured.
func (h *HistoryService) TrackedServerURLs() []string {
	if h == nil || h.stops == nil {
		return nil
	}
	return h.stops.baseURLs()
}

// Close reasons for unmatched-stop episodes. 'not_reported' is a complete
// response that no longer reports the stop; the other two close an episode
// whose agency disappeared, against the agency's last complete response
// (migration 00004).
const (
	closeReasonNotReported    = "not_reported"
	closeReasonAgencyUnlisted = "agency_unlisted"
	closeReasonAgencyRemoved  = "agency_removed"
)

// stopEpisodePlan is what one response changes for one server.
type stopEpisodePlan struct {
	// agencies are the agencies the response is recorded for, without
	// duplicates.
	agencies []models.ObaServer
	// complete holds the agencies whose IDs appear in agencyIDs. Only they
	// open or close episodes.
	complete map[string]bool
	// unlisted holds the recorded agencies missing from agencyIDs. OBA no
	// longer tracks them, so their open episodes close: no later response of
	// theirs is complete, and they would never close otherwise.
	unlisted map[string]bool
	// unlistedCloses are the open episodes closed because their agency left
	// agencyIDs, sorted like closes.
	unlistedCloses []unmatchedStopKey
	opens          []unmatchedStopKey
	closes         []unmatchedStopKey
	still          []unmatchedStopKey
}

// withUnlistedOpenAgencies appends a synthetic recorded entry for every
// agency that has an open episode but is missing from both the response's
// agencyIDs and the recorded agencies. Server-scoped collection records only
// the agencies OBA still reports, so without these an agency that leaves
// agencyIDs would never be marked unlisted and its open episodes would never
// close (#438). An agency OBA still lists is never synthesized: when its
// checks did not run this tick its episodes stay untouched, exactly as
// before. The synthetic entries carry no agency name; ensureAgency keeps the
// existing name when the caller has none.
func withUnlistedOpenAgencies(agencies []models.ObaServer, response *metrics.OBAMetrics, open map[unmatchedStopKey]*openStopEpisode) []models.ObaServer {
	reported := make(map[string]bool, len(response.Data.Entry.AgencyIDs))
	for _, id := range response.Data.Entry.AgencyIDs {
		reported[id] = true
	}
	recorded := make(map[string]bool, len(agencies))
	for _, agency := range agencies {
		recorded[agency.AgencyID] = true
	}
	var missing []string
	for key := range open {
		if !reported[key.agencyID] && !recorded[key.agencyID] {
			missing = append(missing, key.agencyID)
			recorded[key.agencyID] = true
		}
	}
	if len(missing) == 0 {
		return agencies
	}
	sort.Strings(missing)
	out := make([]models.ObaServer, 0, len(agencies)+len(missing))
	out = append(out, agencies...)
	for _, id := range missing {
		out = append(out, models.ObaServer{AgencyID: id})
	}
	return out
}

// planStopEpisodes compares a response with the open episodes in memory.
func planStopEpisodes(open map[unmatchedStopKey]*openStopEpisode, agencies []models.ObaServer, response *metrics.OBAMetrics) stopEpisodePlan {
	entry := response.Data.Entry
	reportedAgencies := make(map[string]bool, len(entry.AgencyIDs))
	for _, id := range entry.AgencyIDs {
		reportedAgencies[id] = true
	}

	plan := stopEpisodePlan{complete: make(map[string]bool), unlisted: make(map[string]bool)}
	seenAgencies := make(map[string]bool, len(agencies))
	reported := make(map[unmatchedStopKey]bool)
	for _, agency := range agencies {
		if agency.AgencyID == "" || seenAgencies[agency.AgencyID] {
			continue
		}
		seenAgencies[agency.AgencyID] = true
		plan.agencies = append(plan.agencies, agency)
		if !reportedAgencies[agency.AgencyID] {
			plan.unlisted[agency.AgencyID] = true
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
	// Episodes of unlisted agencies close separately from closes: no complete
	// response of theirs exists to close them against.
	var unlistedCloses []unmatchedStopKey
	for key := range open {
		if plan.unlisted[key.agencyID] {
			unlistedCloses = append(unlistedCloses, key)
		}
	}
	sort.Slice(unlistedCloses, func(i, j int) bool {
		a, b := unlistedCloses[i], unlistedCloses[j]
		if a.agencyID != b.agencyID {
			return a.agencyID < b.agencyID
		}
		return a.stopID < b.stopID
	})
	plan.unlistedCloses = unlistedCloses
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
// Server-scoped collection records only agencies OBA still reports, so an
// agency that leaves agencyIDs never appears in agencies. For a server-scoped
// entry the open episodes themselves supply those agencies: any episode whose
// agency is missing from agencyIDs closes as agency_unlisted, including when
// OBA stops listing every agency and agencies is empty (#438).
//
// Call it only after a successful fetch: a failed fetch says nothing about
// which IDs were reported. A response that opens or closes nothing, when the
// 10-minute save is not due, does not touch the database.
func (h *HistoryService) RecordOBAMetrics(ctx context.Context, server models.ObaServer, agencies []models.ObaServer, response *metrics.OBAMetrics, observedAt time.Time) {
	if h == nil || h.pool == nil || response == nil {
		return
	}
	if len(agencies) == 0 && !server.IsServerScoped() {
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

	if server.IsServerScoped() {
		agencies = withUnlistedOpenAgencies(agencies, response, entry.open)
	}

	plan := planStopEpisodes(entry.open, agencies, response)
	for _, key := range plan.still {
		entry.open[key].lastReportedAt = observedAt
	}

	closing := make(map[unmatchedStopKey]bool, len(plan.closes)+len(plan.unlistedCloses))
	for _, key := range plan.closes {
		closing[key] = true
	}
	for _, key := range plan.unlistedCloses {
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

	if len(plan.opens) == 0 && len(plan.closes) == 0 && len(plan.unlistedCloses) == 0 && !saveDue {
		return
	}

	var opened map[unmatchedStopKey]*openStopEpisode
	committed := h.write(ctx, obaMetricsStep, publicationTags(server), func(q *dbgen.Queries) error {
		opened = nil
		if len(plan.opens) > 0 || len(plan.closes) > 0 || len(plan.unlistedCloses) > 0 {
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
		// The commit may have reached the database even though the client
		// saw an error, so memory can no longer be trusted: reload open
		// episodes on the next poll, which then records any difference with
		// its own time.
		entry.loaded = false
		return
	}
	for key, episode := range opened {
		entry.open[key] = episode
	}
	for _, key := range plan.closes {
		delete(entry.open, key)
	}
	for _, key := range plan.unlistedCloses {
		delete(entry.open, key)
	}
	// With no episode open before this poll, everything open now was
	// written exactly, so the save clock restarts too.
	if saveDue || len(others) == 0 {
		entry.savedAt = observedAt
	}
}

// loadOpenEpisodes replaces entry's open episodes with the server's open
// episodes in the database. It reports whether it succeeded; on failure entry
// is unchanged and stays unloaded, and the next call tries again.
func (h *HistoryService) loadOpenEpisodes(ctx context.Context, server models.ObaServer, baseURL string, entry *serverStopEpisodes, now time.Time) bool {
	rows, err := dbgen.New(h.pool).ListOpenUnmatchedStopEpisodesForServer(ctx, baseURL)
	if err != nil {
		reportWriteFailure(h.logger, unmatchedStopLoadStep, publicationTags(server), fmt.Errorf("load open unmatched-stop episodes: %w", err))
		return false
	}
	// Replace memory, not merge into it: after a failed write it may hold an
	// episode the database has already closed. But a quiet poll may have
	// moved an episode's lastReportedAt past the value last saved, so keep
	// the later time when memory still holds the same episode.
	previous := entry.open
	entry.open = make(map[unmatchedStopKey]*openStopEpisode, len(rows))
	for _, row := range rows {
		key := unmatchedStopKey{agencyID: row.ExternalAgencyID, stopID: row.ObaStopID}
		lastReportedAt := row.LastReportedAt
		if old, ok := previous[key]; ok && old.episodeID == row.UnmatchedStopEpisodeID && old.lastReportedAt.After(lastReportedAt) {
			lastReportedAt = old.lastReportedAt
		}
		entry.open[key] = &openStopEpisode{
			episodeID:      row.UnmatchedStopEpisodeID,
			reportedStopID: row.ReportedStopID,
			agencyID:       row.AgencyID,
			lastReportedAt: lastReportedAt,
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

	version, hasVersion := h.static.currentVersion(server.ServerKey())
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
		// While a newer snapshot is visible but its version is not recorded
		// yet, link nothing; trigger 2 adds the link.
		if hasVersion && stops.matches(version, key.agencyID) {
			stop, found := stops.lookup(key.agencyID, key.stopID)
			if err := linkOpenedEpisodeMetadata(ctx, q, version.id, episode, stop, found); err != nil {
				return nil, fmt.Errorf("server %q: metadata for unmatched stop %q: %w", server.ServerName, key.stopID, err)
			}
		}
		opened[key] = episode
	}

	for _, key := range plan.closes {
		episode := open[key]
		if err := q.CloseUnmatchedStopEpisode(ctx, dbgen.CloseUnmatchedStopEpisodeParams{
			ClosingResponseAgencyID: responseAgencyIDs[key.agencyID],
			CloseReason:             ptr(closeReasonNotReported),
			LastReportedAt:          episode.lastReportedAt,
			UnmatchedStopEpisodeID:  episode.episodeID,
		}); err != nil {
			return nil, fmt.Errorf("server %q: close unmatched stop %q: %w", server.ServerName, key.stopID, err)
		}
	}
	// Unlisted agencies have no complete response in this poll to close
	// against, so their episodes close against their last complete one.
	if len(plan.unlistedCloses) > 0 {
		episodes := make([]*openStopEpisode, 0, len(plan.unlistedCloses))
		for _, key := range plan.unlistedCloses {
			episodes = append(episodes, open[key])
		}
		if err := closeDisappearedAgencyEpisodes(ctx, q, serverID, episodes, closeReasonAgencyUnlisted); err != nil {
			return nil, fmt.Errorf("server %q: close unmatched stops of unlisted agencies: %w", server.ServerName, err)
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
	times := make([]time.Time, len(episodes))
	for i, episode := range episodes {
		ids[i] = episode.episodeID
		times[i] = episode.lastReportedAt
	}
	if err := q.SaveUnmatchedStopLastReported(ctx, dbgen.SaveUnmatchedStopLastReportedParams{
		EpisodeIds:     ids,
		LastReportedAt: times,
	}); err != nil {
		return fmt.Errorf("save last_reported_at of %d unmatched-stop episodes: %w", len(episodes), err)
	}
	return nil
}

// FlushServer saves the last_reported_at of a removed server's open episodes,
// then drops the server from memory. app.OnConfigUpdated calls it before
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

// FlushAgencies closes the open episodes of a server's agencies that are no
// longer configured, then drops them from memory. app.OnConfigUpdated calls
// it for a base URL that stays configured while some of its agency-mode
// entries are removed: no poll records those agencies any more, so their
// episodes would otherwise stay in memory for good. Closing the rows, not
// just saving last_reported_at, keeps a later reload from loading them back
// (#438). An episode leaves memory only once its close is on disk: when the
// write fails the row is still open in the database, so the episode stays
// here for the next config update to retry, and a restart reloads it either
// way.
func (h *HistoryService) FlushAgencies(ctx context.Context, baseURL string, keep map[string]bool) {
	if h == nil || h.pool == nil || h.stops == nil {
		return
	}
	entry, ok := h.stops.lookup(utils.SanitizeServerURL(baseURL))
	if !ok {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	h.closeRemovedAgenciesLocked(ctx, entry, keep)
}

// ReconcileConfiguredAgencies is FlushAgencies for a server that may not have
// been polled yet: it loads the server's open episodes from the database
// first. main calls it at startup, because an agency removed while Watchdog
// was down leaves its episodes open in the database, and a --config-file
// deployment has no refresh loop to close them later (#438).
func (h *HistoryService) ReconcileConfiguredAgencies(ctx context.Context, server models.ObaServer, keep map[string]bool) {
	if h == nil || h.pool == nil || h.stops == nil {
		return
	}
	baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if baseURL == "" {
		return
	}
	entry := h.stops.forServer(baseURL, server)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.server = server
	if !entry.loaded && !h.loadOpenEpisodes(ctx, server, baseURL, entry, time.Now().UTC()) {
		return
	}
	h.closeRemovedAgenciesLocked(ctx, entry, keep)
}

// closeRemovedAgenciesLocked closes entry's open episodes whose agency is not
// in keep, each against its agency's last complete observation. Entry must be
// locked and loaded. Episodes whose close write fails stay in memory so the
// next call retries them.
func (h *HistoryService) closeRemovedAgenciesLocked(ctx context.Context, entry *serverStopEpisodes, keep map[string]bool) {
	var removed []unmatchedStopKey
	var episodes []*openStopEpisode
	for key, episode := range entry.open {
		if !keep[key.agencyID] {
			removed = append(removed, key)
			episodes = append(episodes, episode)
		}
	}
	if len(removed) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	committed := h.write(ctx, unmatchedStopFlushStep, publicationTags(entry.server), func(q *dbgen.Queries) error {
		serverID, err := ensureServer(ctx, q, entry.server, h.resolver)
		if err != nil {
			return err
		}
		return closeDisappearedAgencyEpisodes(ctx, q, serverID, episodes, closeReasonAgencyRemoved)
	})
	if committed {
		for _, key := range removed {
			delete(entry.open, key)
		}
	}
}

// ptr returns a pointer to v, for nullable query parameters.
func ptr[T any](v T) *T { return &v }

// closeDisappearedAgencyEpisodes closes episodes whose agency no longer has
// complete responses, grouping them by agency so each agency's episodes close
// against one shared closing response.
func closeDisappearedAgencyEpisodes(ctx context.Context, q *dbgen.Queries, serverID int64, episodes []*openStopEpisode, reason string) error {
	byAgency := make(map[int64][]*openStopEpisode)
	var order []int64
	for _, episode := range episodes {
		if _, ok := byAgency[episode.agencyID]; !ok {
			order = append(order, episode.agencyID)
		}
		byAgency[episode.agencyID] = append(byAgency[episode.agencyID], episode)
	}
	for _, agencyID := range order {
		group := byAgency[agencyID]
		closingID, err := disappearanceClosingResponse(ctx, q, serverID, agencyID, group)
		if err != nil {
			return err
		}
		for _, episode := range group {
			if err := q.CloseUnmatchedStopEpisode(ctx, dbgen.CloseUnmatchedStopEpisodeParams{
				ClosingResponseAgencyID: closingID,
				CloseReason:             ptr(reason),
				LastReportedAt:          episode.lastReportedAt,
				UnmatchedStopEpisodeID:  episode.episodeID,
			}); err != nil {
				return fmt.Errorf("close episode %d (%s): %w", episode.episodeID, reason, err)
			}
		}
	}
	return nil
}

// disappearanceClosingResponse picks the response an agency's episodes close
// against: the newest complete response stored for it, unless a later poll
// still reported one of the closing stops. Such a poll was a complete
// observation — last_reported_at advances only when the agency's response is
// complete and reports the stop — that quiet polls never store, so it is
// recorded now and the episodes close against it. Without that, closed_at
// (the closing response's time) could precede an episode's last_reported_at.
func disappearanceClosingResponse(ctx context.Context, q *dbgen.Queries, serverID, agencyID int64, episodes []*openStopEpisode) (int64, error) {
	last, err := q.GetLastCompleteResponseAgency(ctx, agencyID)
	if err != nil {
		return 0, fmt.Errorf("last complete response of agency %d: %w", agencyID, err)
	}
	lastSeen := last.ObservedAt
	for _, episode := range episodes {
		if episode.lastReportedAt.After(lastSeen) {
			lastSeen = episode.lastReportedAt
		}
	}
	if !lastSeen.After(last.ObservedAt) {
		return last.ResponseAgencyID, nil
	}
	responseID, err := q.InsertOBAMetricsResponse(ctx, dbgen.InsertOBAMetricsResponseParams{
		ServerID:   serverID,
		ObservedAt: lastSeen,
	})
	if err != nil {
		return 0, fmt.Errorf("record the last complete observation of agency %d: %w", agencyID, err)
	}
	closingID, err := q.InsertOBAMetricsResponseAgency(ctx, dbgen.InsertOBAMetricsResponseAgencyParams{
		ResponseID:       responseID,
		AgencyID:         agencyID,
		ResponseComplete: true,
	})
	if err != nil {
		return 0, fmt.Errorf("record the last complete observation of agency %d: %w", agencyID, err)
	}
	return closingID, nil
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

// flushEntry saves the in-memory last_reported_at of entry's open episodes.
// It also runs for an entry left unloaded by a failed write: memory may then
// hold an episode the database already closed, but the save only updates
// open episodes and only moves a time forward, so that is harmless, while
// skipping it would lose up to the save interval of last_reported_at.
func (h *HistoryService) flushEntry(ctx context.Context, entry *serverStopEpisodes) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if len(entry.open) == 0 {
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

// cleanText makes GTFS text storable: PostgreSQL rejects invalid UTF-8 and
// NUL, and one rejected row would roll back the whole write.
func cleanText(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
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
	name := cleanText(stop.Name)
	facts := stopFacts{found: true, name: &name}
	if stop.Latitude != nil && stop.Longitude != nil && geo.IsValidLatLon(*stop.Latitude, *stop.Longitude) {
		lat, lon := *stop.Latitude, *stop.Longitude
		facts.latitude, facts.longitude = &lat, &lon
	}
	if stop.Parent != nil {
		parentID := cleanText(stop.Parent.Id)
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
	// data pins the snapshot read for each key, so matches and lookup see
	// the same one.
	data  map[string]*models.StaticData
	byKey map[string]map[string]remoteGtfs.Stop
}

func newStaticStopIndex(store *gtfs.StaticStore, server models.ObaServer) *staticStopIndex {
	return &staticStopIndex{
		store:  store,
		server: server,
		data:   make(map[string]*models.StaticData),
		byKey:  make(map[string]map[string]remoteGtfs.Stop),
	}
}

// lookup finds a stop in the agency's snapshot, falling back to the server
// key's snapshot in server mode.
func (x *staticStopIndex) lookup(agencyID, stopID string) (remoteGtfs.Stop, bool) {
	for _, key := range x.keys(agencyID) {
		if stop, ok := x.stops(key)[stopID]; ok {
			return stop, true
		}
	}
	return remoteGtfs.Stop{}, false
}

func (x *staticStopIndex) keys(agencyID string) []string {
	keys := []string{models.ServerKey(x.server.ObaBaseURL, agencyID)}
	if x.server.IsServerScoped() {
		keys = append(keys, x.server.ServerKey())
	}
	return keys
}

// matches reports whether the snapshots a lookup for agencyID reads are the
// ones version was recorded from.
func (x *staticStopIndex) matches(version staticVersion, agencyID string) bool {
	for _, key := range x.keys(agencyID) {
		if x.snapshot(key) != version.snapshots[key] {
			return false
		}
	}
	return true
}

func (x *staticStopIndex) snapshot(serverKey string) *models.StaticData {
	if data, ok := x.data[serverKey]; ok {
		return data
	}
	data := staticSnapshot(x.store, serverKey)
	x.data[serverKey] = data
	return data
}

// staticSnapshot returns the snapshot stored under serverKey, or nil.
func staticSnapshot(store *gtfs.StaticStore, serverKey string) *models.StaticData {
	if store == nil {
		return nil
	}
	data, _ := store.Get(serverKey)
	return data
}

func (x *staticStopIndex) stops(serverKey string) map[string]remoteGtfs.Stop {
	if stops, ok := x.byKey[serverKey]; ok {
		return stops
	}
	var stops map[string]remoteGtfs.Stop
	if data := x.snapshot(serverKey); data != nil {
		stops = make(map[string]remoteGtfs.Stop, len(data.Stops))
		for _, stop := range data.Stops {
			// Keep the first occurrence, as gtfs.GetStopLocationsByIDs does.
			if _, seen := stops[stop.Id]; !seen {
				stops[stop.Id] = stop
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

	version, ok := h.static.currentVersion(server.ServerKey())
	if !ok || version.id != versionID {
		// A newer publication replaced this one and runs its own trigger.
		return
	}
	stops := newStaticStopIndex(h.staticStore, server)
	h.write(ctx, unmatchedStopMetadataStep, publicationTags(server), func(q *dbgen.Queries) error {
		for _, c := range episodes {
			if !stops.matches(version, c.key.agencyID) {
				continue
			}
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
		id := cleanText(cluster.StationID)
		stationID = &id
		stationName = rootStationName(stop, cluster.StationID) // original ID
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
			name := cleanText(s.Name)
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
