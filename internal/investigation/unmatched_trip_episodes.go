package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindUnmatchedTripStaticContexts is the include kind for a trip episode's
// static-contexts collection (E19 registers the loader).
const kindUnmatchedTripStaticContexts resourceKind = "unmatched_trip_static_contexts"

// unmatchedTripEpisodeIncludable names the links this resource can include.
var unmatchedTripEpisodeIncludable = map[string]resourceKind{
	"reported_trip":    kindReportedTrip,
	"agency":           kindAgency,
	"server":           kindServer,
	"opening_response": kindOBAMetricsResponse,
	"closing_response": kindOBAMetricsResponse,
	"static_contexts":  kindUnmatchedTripStaticContexts,
}

// unmatchedTripEpisode is the representation of one unmatched-trip episode (E18).
type unmatchedTripEpisode struct {
	UnmatchedTripEpisodeID int64    `json:"unmatched_trip_episode_id"`
	Status                 string   `json:"status"`
	TripID                 string   `json:"trip_id"`
	AgencyID               int64    `json:"agency_id"`
	ServerID               int64    `json:"server_id"`
	OpenedAt               apiTime  `json:"opened_at"`
	LastReportedAt         apiTime  `json:"last_reported_at"`
	ClosedAt               *apiTime `json:"closed_at"`
	DurationSeconds        *int64   `json:"duration_seconds"`
	CloseReason            *string  `json:"close_reason"`
	PreviousEpisodeCount   int64    `json:"previous_episode_count"`
	PreviousClosedAt       *apiTime `json:"previous_closed_at"`
	Links                  links    `json:"links"`
}

// unmatchedTripEpisodeBody is the E18 response.
type unmatchedTripEpisodeBody struct {
	unmatchedTripEpisode
	Included included `json:"included,omitzero"`
}

// unmatchedTripEpisodesBody is the E17 and E13 response.
type unmatchedTripEpisodesBody struct {
	Items    []unmatchedTripEpisode `json:"items"`
	Links    links                  `json:"links"`
	Included included               `json:"included,omitzero"`
}

// unmatchedTripEpisodeLinks builds an episode's links; closing_response only exists once it closed.
func unmatchedTripEpisodeLinks(row dbgen.UnmatchedTripEpisodeView) itemLinks {
	self := "/v1/unmatched-trip-episodes/" + strconv.FormatInt(row.UnmatchedTripEpisodeID, 10)
	l := itemLinks{
		"self": {URL: self},
		"reported_trip": {
			Kind: kindReportedTrip,
			ID:   row.ReportedTripID,
			URL:  "/v1/reported-trips/" + strconv.FormatInt(row.ReportedTripID, 10),
		},
		"agency": {
			Kind: kindAgency,
			ID:   row.AgencyID,
			URL:  "/v1/agencies/" + strconv.FormatInt(row.AgencyID, 10),
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
		"opening_response": {
			Kind: kindOBAMetricsResponse,
			ID:   row.OpeningResponseID,
			URL:  "/v1/oba-metrics-responses/" + strconv.FormatInt(row.OpeningResponseID, 10),
		},
		"static_contexts": {
			Kind: kindUnmatchedTripStaticContexts,
			ID:   row.UnmatchedTripEpisodeID,
			URL:  self + "/static-contexts",
		},
	}
	if row.ClosingResponseID != nil {
		l["closing_response"] = related{
			Kind: kindOBAMetricsResponse,
			ID:   *row.ClosingResponseID,
			URL:  "/v1/oba-metrics-responses/" + strconv.FormatInt(*row.ClosingResponseID, 10),
		}
	}
	return l
}

// unmatchedTripEpisodeRepresentation maps one view row to its E18 representation.
func unmatchedTripEpisodeRepresentation(row dbgen.UnmatchedTripEpisodeView, l itemLinks) unmatchedTripEpisode {
	ep := unmatchedTripEpisode{
		UnmatchedTripEpisodeID: row.UnmatchedTripEpisodeID,
		Status:                 "open",
		TripID:                 row.TripID,
		AgencyID:               row.AgencyID,
		ServerID:               row.ServerID,
		OpenedAt:               newAPITime(row.OpenedAt),
		LastReportedAt:         newAPITime(row.LastReportedAt),
		ClosedAt:               newOptionalAPITime(row.ClosedAt),
		CloseReason:            row.CloseReason,
		PreviousEpisodeCount:   row.PreviousEpisodeCount,
		PreviousClosedAt:       newOptionalAPITime(row.PreviousClosedAt),
		Links:                  l.links(),
	}
	if row.ClosingResponseAgencyID != nil {
		ep.Status = "closed"
	}
	if row.ClosedAt != nil {
		seconds := int64(row.ClosedAt.Sub(row.OpenedAt) / time.Second)
		ep.DurationSeconds = &seconds
	}
	return ep
}

// getUnmatchedTripEpisode serves GET /v1/unmatched-trip-episodes/:episode_id.
func (s *InvestigationService) getUnmatchedTripEpisode(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "episode_id")
	if !ok {
		s.fail(w, r, pgx.ErrNoRows)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, unmatchedTripEpisodeIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body unmatchedTripEpisodeBody
	var closedAt *time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetUnmatchedTripEpisode(ctx, id)
		if err != nil {
			return err
		}
		closedAt = row.ClosedAt
		l := unmatchedTripEpisodeLinks(row)
		body.unmatchedTripEpisode = unmatchedTripEpisodeRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cache := cacheShort()
	if closedAt != nil && !inc.requested() {
		cache = cacheImmutable(*closedAt)
	}
	s.writeJSON(w, r, body, cache)
}

// tripStaticContextIncludable accepts gtfs_static_version once its loader is registered.
var tripStaticContextIncludable = map[string]resourceKind{
	"gtfs_static_version": "gtfs_static_version",
}

// tripStaticContext is one item of E19: what Watchdog's static GTFS said about the trip for part of the episode.
type tripStaticContext struct {
	TripMetadataVersionID int64    `json:"trip_metadata_version_id"`
	Source                string   `json:"source"`
	StaticTripFound       bool     `json:"static_trip_found"`
	RouteID               *string  `json:"route_id"`
	ServiceID             *string  `json:"service_id"`
	BlockID               *string  `json:"block_id"`
	AppliesFrom           apiTime  `json:"applies_from"`
	AppliesUntil          *apiTime `json:"applies_until"`
	Links                 links    `json:"links"`
}

// tripStaticContexts is the E19 representation, which the static_contexts loader also returns.
type tripStaticContexts struct {
	Items []tripStaticContext `json:"items"`
	Links links               `json:"links"`
}

// tripStaticContextsBody is the E19 response.
type tripStaticContextsBody struct {
	tripStaticContexts
	Included included `json:"included,omitzero"`
}

// tripStaticContextList maps one episode's rows, oldest published first, to E19 and each item's links.
func tripStaticContextList(episodeID int64, rows []dbgen.ListUnmatchedTripEpisodeStaticContextsRow) (tripStaticContexts, []itemLinks) {
	episode := "/v1/unmatched-trip-episodes/" + strconv.FormatInt(episodeID, 10)
	list := tripStaticContexts{
		Items: make([]tripStaticContext, 0, len(rows)),
		Links: links{"self": episode + "/static-contexts", "episode": episode},
	}
	related := make([]itemLinks, 0, len(rows))
	for i, row := range rows {
		l := itemLinks{"gtfs_static_version": {
			Kind: "gtfs_static_version",
			ID:   row.GtfsStaticVersionID,
			URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(row.GtfsStaticVersionID, 10),
		}}
		until := row.ClosedAt
		if i+1 < len(rows) {
			next := tripAppliesFrom(rows[i+1])
			until = &next
		}
		list.Items = append(list.Items, tripStaticContext{
			TripMetadataVersionID: row.TripMetadataVersionID,
			Source:                "watchdog_static_gtfs",
			StaticTripFound:       row.StaticTripFound,
			RouteID:               row.RouteID,
			ServiceID:             row.ServiceID,
			BlockID:               row.BlockID,
			AppliesFrom:           newAPITime(tripAppliesFrom(row)),
			AppliesUntil:          newOptionalAPITime(until),
			Links:                 l.links(),
		})
		related = append(related, l)
	}
	return list, related
}

// tripAppliesFrom is the later of the static version's publication and the episode's opening.
func tripAppliesFrom(row dbgen.ListUnmatchedTripEpisodeStaticContextsRow) time.Time {
	if row.PublishedAt.After(row.OpenedAt) {
		return row.PublishedAt
	}
	return row.OpenedAt
}

// loadTripStaticContexts returns E19 for each episode in ids, in one query.
func loadTripStaticContexts(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListUnmatchedTripEpisodeStaticContexts(ctx, ids)
	if err != nil {
		return nil, err
	}
	byEpisode := make(map[int64][]dbgen.ListUnmatchedTripEpisodeStaticContextsRow, len(ids))
	for _, row := range rows {
		byEpisode[row.UnmatchedTripEpisodeID] = append(byEpisode[row.UnmatchedTripEpisodeID], row)
	}
	out := make(map[int64]any, len(ids))
	for _, id := range ids {
		out[id], _ = tripStaticContextList(id, byEpisode[id])
	}
	return out, nil
}

// listUnmatchedTripEpisodeStaticContexts serves GET /v1/unmatched-trip-episodes/:episode_id/static-contexts.
func (s *InvestigationService) listUnmatchedTripEpisodeStaticContexts(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "episode_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, tripStaticContextIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body tripStaticContextsBody
	var closedAt *time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		episode, err := q.GetUnmatchedTripEpisode(ctx, id)
		if err != nil {
			return err
		}
		closedAt = episode.ClosedAt
		rows, err := q.ListUnmatchedTripEpisodeStaticContexts(ctx, []int64{id})
		if err != nil {
			return err
		}
		var related []itemLinks
		body.tripStaticContexts, related = tripStaticContextList(id, rows)
		body.Included, err = s.loadIncluded(ctx, q, related, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cache := cacheShort()
	if closedAt != nil {
		cache = cacheImmutable(*closedAt)
	}
	s.writeJSON(w, r, body, cache)
}

// unmatchedTripEpisodePage reads one page of ListUnmatchedTripEpisodes for E17 and E13.
// The items are never nil, so an empty page encodes as [] rather than null.
func (s *InvestigationService) unmatchedTripEpisodePage(ctx context.Context, q *dbgen.Queries, r *http.Request,
	params dbgen.ListUnmatchedTripEpisodesParams, p page, inc include) (unmatchedTripEpisodesBody, error) {
	var body unmatchedTripEpisodesBody
	rows, err := q.ListUnmatchedTripEpisodes(ctx, params)
	if err != nil {
		return body, err
	}
	rows, hasMore := trimPage(rows, p)
	body.Items = make([]unmatchedTripEpisode, 0, len(rows))
	all := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := unmatchedTripEpisodeLinks(row)
		all = append(all, l)
		body.Items = append(body.Items, unmatchedTripEpisodeRepresentation(row, l))
	}
	var lastID int64
	if len(rows) > 0 {
		lastID = rows[len(rows)-1].UnmatchedTripEpisodeID
	}
	body.Links = pageLinks(r, lastID, hasMore)
	body.Included, err = s.loadIncluded(ctx, q, all, inc)
	return body, err
}

// listUnmatchedTripEpisodes serves GET /v1/unmatched-trip-episodes. Without a
// window it covers the last 24 hours, except with status=open, where open
// episodes need no window.
func (s *InvestigationService) listUnmatchedTripEpisodes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "server_id", "agency_id", "trip_id", "status", "active_from", "active_to",
		"close_reason", "static_found", "include", "page_size", "cursor")
	params := dbgen.ListUnmatchedTripEpisodesParams{
		ServerID:    paramID(values, "server_id", &bad),
		AgencyID:    paramID(values, "agency_id", &bad),
		ObaTripID:   paramString(values, "trip_id", &bad),
		Status:      paramEnum(values, "status", []string{"open", "closed"}, &bad),
		CloseReason: paramEnum(values, "close_reason", unmatchedCloseReasons, &bad),
		StaticFound: paramBool(values, "static_found", &bad),
	}
	// A trip ID is only unique within one agency, so it needs agency_id.
	if params.ObaTripID != nil && !values.Has("agency_id") {
		bad.add("trip_id", "requires agency_id")
	}
	statusOpen := params.Status != nil && *params.Status == "open"
	win := s.parseWindow(values, activeWindowOptions(statusOpen, false), &bad)
	inc := s.parseInclude(values, unmatchedTripEpisodeIncludable, &bad)
	p := parsePage(values, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}
	params.ActiveFrom, params.ActiveTo = win.From, win.To
	params.AfterID, params.PageLimit = p.AfterID, p.Limit()

	var body unmatchedTripEpisodesBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		body, err = s.unmatchedTripEpisodePage(ctx, q, r, params, p, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}
