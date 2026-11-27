package investigation

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

const (
	kindOBAMetricsResponse          resourceKind = "oba_metrics_response"
	kindUnmatchedStopStaticContexts resourceKind = "unmatched_stop_static_contexts"
)

// unmatchedStopEpisodeIncludable uses the kinds the agency, server and reported-stop endpoints register.
var unmatchedStopEpisodeIncludable = map[string]resourceKind{
	"reported_stop":    kindReportedStop,
	"agency":           kindAgency,
	"server":           kindServer,
	"opening_response": kindOBAMetricsResponse,
	"closing_response": kindOBAMetricsResponse,
	"static_contexts":  kindUnmatchedStopStaticContexts,
}

// unmatchedStopEpisode is the representation of one unmatched-stop episode (E14, E15).
type unmatchedStopEpisode struct {
	UnmatchedStopEpisodeID int64    `json:"unmatched_stop_episode_id"`
	Status                 string   `json:"status"`
	StopID                 string   `json:"stop_id"`
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

// unmatchedStopEpisodeBody is the E15 response.
type unmatchedStopEpisodeBody struct {
	unmatchedStopEpisode
	Included included `json:"included,omitzero"`
}

// unmatchedStopEpisodesBody is the E14 response.
type unmatchedStopEpisodesBody struct {
	Items    []unmatchedStopEpisode `json:"items"`
	Links    links                  `json:"links"`
	Included included               `json:"included,omitzero"`
}

// unmatchedStopEpisodeLinks builds an episode's links; closing_response only exists once it closed.
func unmatchedStopEpisodeLinks(row dbgen.UnmatchedStopEpisodeView) itemLinks {
	self := "/v1/unmatched-stop-episodes/" + strconv.FormatInt(row.UnmatchedStopEpisodeID, 10)
	l := itemLinks{
		"self": {URL: self},
		"reported_stop": {
			Kind: kindReportedStop,
			ID:   row.ReportedStopID,
			URL:  "/v1/reported-stops/" + strconv.FormatInt(row.ReportedStopID, 10),
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
			Kind: kindUnmatchedStopStaticContexts,
			ID:   row.UnmatchedStopEpisodeID,
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

// unmatchedStopEpisodeRepresentation maps one view row to its E14 and E15 representation.
func unmatchedStopEpisodeRepresentation(row dbgen.UnmatchedStopEpisodeView, l itemLinks) unmatchedStopEpisode {
	ep := unmatchedStopEpisode{
		UnmatchedStopEpisodeID: row.UnmatchedStopEpisodeID,
		Status:                 "open",
		StopID:                 row.StopID,
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

// unmatchedStopEpisodePage reads one page of ListUnmatchedStopEpisodes for E14 and E10.
func (s *InvestigationService) unmatchedStopEpisodePage(ctx context.Context, q *dbgen.Queries, r *http.Request,
	params dbgen.ListUnmatchedStopEpisodesParams, p page, inc include) (unmatchedStopEpisodesBody, error) {
	var body unmatchedStopEpisodesBody
	rows, err := q.ListUnmatchedStopEpisodes(ctx, params)
	if err != nil {
		return body, err
	}
	rows, hasMore := trimPage(rows, p)
	body.Items = make([]unmatchedStopEpisode, 0, len(rows))
	all := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := unmatchedStopEpisodeLinks(row)
		all = append(all, l)
		body.Items = append(body.Items, unmatchedStopEpisodeRepresentation(row, l))
	}
	var lastID int64
	if len(rows) > 0 {
		lastID = rows[len(rows)-1].UnmatchedStopEpisodeID
	}
	body.Links = pageLinks(r, lastID, hasMore)
	body.Included, err = s.loadIncluded(ctx, q, all, inc)
	return body, err
}

// getUnmatchedStopEpisode serves GET /v1/unmatched-stop-episodes/:episode_id.
func (s *InvestigationService) getUnmatchedStopEpisode(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "episode_id")
	if !ok {
		s.fail(w, r, pgx.ErrNoRows)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, unmatchedStopEpisodeIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body unmatchedStopEpisodeBody
	var closedAt *time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetUnmatchedStopEpisode(ctx, id)
		if err != nil {
			return err
		}
		closedAt = row.ClosedAt
		l := unmatchedStopEpisodeLinks(row)
		body.unmatchedStopEpisode = unmatchedStopEpisodeRepresentation(row, l)
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

// listUnmatchedStopEpisodes serves GET /v1/unmatched-stop-episodes.
func (s *InvestigationService) listUnmatchedStopEpisodes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "server_id", "agency_id", "stop_id", "status", "active_from", "active_to",
		"opened_from", "opened_to", "open_at", "opening_response_id", "closing_response_id",
		"gtfs_static_version_id", "recurrence", "cell_id", "root_station_id", "scheme_version",
		"close_reason", "static_found", "include", "page_size", "cursor")
	params := dbgen.ListUnmatchedStopEpisodesParams{
		ServerID:            paramID(values, "server_id", &bad),
		AgencyID:            paramID(values, "agency_id", &bad),
		ObaStopID:           paramString(values, "stop_id", &bad),
		Status:              paramEnum(values, "status", []string{"open", "closed"}, &bad),
		CloseReason:         paramEnum(values, "close_reason", []string{"not_reported"}, &bad),
		StaticFound:         paramBool(values, "static_found", &bad),
		OpeningResponseID:   paramID(values, "opening_response_id", &bad),
		ClosingResponseID:   paramID(values, "closing_response_id", &bad),
		GtfsStaticVersionID: paramID(values, "gtfs_static_version_id", &bad),
		Recurrence:          paramEnum(values, "recurrence", []string{"has_previous_episode", "no_previous_episode"}, &bad),
		CellID:              paramString(values, "cell_id", &bad),
		RootStationID:       paramString(values, "root_station_id", &bad),
		SchemeVersion:       paramString(values, "scheme_version", &bad),
	}
	if params.ObaStopID != nil && !values.Has("agency_id") {
		bad.add("stop_id", "requires agency_id")
	}
	hasOpenedWindow := values.Has("opened_from") || values.Has("opened_to")
	hasOpenAt := values.Has("open_at")
	replaced := hasOpenedWindow || hasOpenAt
	statusOpen := params.Status != nil && *params.Status == "open"
	win := s.parseWindow(values, activeWindowOptions(statusOpen, replaced), &bad)
	openedWin := s.parseWindow(values, openedWindowOptions(), &bad)
	openAt := paramTime(values, "open_at", &bad)
	notFuture("open_at", openAt, s.now(), &bad)
	inc := s.parseInclude(values, unmatchedStopEpisodeIncludable, &bad)
	p := parsePage(values, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}
	params.ActiveFrom, params.ActiveTo = win.From, win.To
	params.OpenedFrom, params.OpenedTo = openedWin.From, openedWin.To
	params.OpenAt = openAt
	params.AfterID, params.PageLimit = p.AfterID, p.Limit()

	var body unmatchedStopEpisodesBody
	var schemeErr *invalidParam
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		if values.Has("scheme_version") || values.Has("cell_id") || values.Has("root_station_id") {
			if values.Has("scheme_version") {
				sv := *params.SchemeVersion
				_, err := q.GetClusterScheme(ctx, sv)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						schemeErr = &invalidParam{Name: "scheme_version", Reason: "unknown cluster scheme"}
						return nil
					}
					return err
				}
				params.SchemeVersion = &sv
			} else {
				schemes, err := q.ListClusterSchemes(ctx)
				if err != nil {
					return err
				}
				if len(schemes) == 0 {
					schemeErr = &invalidParam{Name: "scheme_version", Reason: "unknown cluster scheme"}
					return nil
				}
				params.SchemeVersion = &schemes[0].SchemeVersion
			}
		}
		var err error
		body, err = s.unmatchedStopEpisodePage(ctx, q, r, params, p, inc)
		return err
	})
	if schemeErr != nil {
		writeInvalidParams(w, invalidParams{*schemeErr})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// stopStaticContextIncludable is what a stop static context accepts in include.
var stopStaticContextIncludable = map[string]resourceKind{
	"gtfs_static_version": kindGTFSStaticVersion,
}

// stopStaticContext is one item of E16: what Watchdog's static GTFS said about the stop for part of the episode.
type stopStaticContext struct {
	StopMetadataVersionID int64    `json:"stop_metadata_version_id"`
	Source                string   `json:"source"`
	StaticStopFound       bool     `json:"static_stop_found"`
	StopName              *string  `json:"stop_name"`
	Latitude              *float64 `json:"latitude"`
	Longitude             *float64 `json:"longitude"`
	ParentStationID       *string  `json:"parent_station_id"`
	LocationType          *int16   `json:"location_type"`
	AppliesFrom           apiTime  `json:"applies_from"`
	AppliesUntil          *apiTime `json:"applies_until"`
	Links                 links    `json:"links"`
}

// stopStaticContexts is the E16 representation, which the static_contexts loader also returns.
type stopStaticContexts struct {
	Items []stopStaticContext `json:"items"`
	Links links               `json:"links"`
}

// stopStaticContextsBody is the E16 response.
type stopStaticContextsBody struct {
	stopStaticContexts
	Included included `json:"included,omitzero"`
}

// stopStaticContextList maps one episode's rows, oldest published first, to E16 and each item's links.
func stopStaticContextList(episodeID int64, rows []dbgen.ListUnmatchedStopEpisodeStaticContextsRow) (stopStaticContexts, []itemLinks) {
	episode := "/v1/unmatched-stop-episodes/" + strconv.FormatInt(episodeID, 10)
	list := stopStaticContexts{
		Items: make([]stopStaticContext, 0, len(rows)),
		Links: links{"self": episode + "/static-contexts", "episode": episode},
	}
	related := make([]itemLinks, 0, len(rows))
	for i, row := range rows {
		l := itemLinks{"gtfs_static_version": {
			Kind: kindGTFSStaticVersion,
			ID:   row.GtfsStaticVersionID,
			URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(row.GtfsStaticVersionID, 10),
		}}
		until := row.ClosedAt
		if i+1 < len(rows) {
			next := appliesFrom(rows[i+1])
			until = &next
		}
		list.Items = append(list.Items, stopStaticContext{
			StopMetadataVersionID: row.StopMetadataVersionID,
			Source:                "watchdog_static_gtfs",
			StaticStopFound:       row.StaticStopFound,
			StopName:              row.StopName,
			Latitude:              row.Latitude,
			Longitude:             row.Longitude,
			ParentStationID:       row.ParentStationID,
			LocationType:          row.LocationType,
			AppliesFrom:           newAPITime(appliesFrom(row)),
			AppliesUntil:          newOptionalAPITime(until),
			Links:                 l.links(),
		})
		related = append(related, l)
	}
	return list, related
}

// appliesFrom is the later of the static version's publication and the episode's opening.
func appliesFrom(row dbgen.ListUnmatchedStopEpisodeStaticContextsRow) time.Time {
	if row.PublishedAt.After(row.OpenedAt) {
		return row.PublishedAt
	}
	return row.OpenedAt
}

// loadStopStaticContexts returns E16 for each episode in ids, in one query.
func loadStopStaticContexts(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListUnmatchedStopEpisodeStaticContexts(ctx, ids)
	if err != nil {
		return nil, err
	}
	byEpisode := make(map[int64][]dbgen.ListUnmatchedStopEpisodeStaticContextsRow, len(ids))
	for _, row := range rows {
		byEpisode[row.UnmatchedStopEpisodeID] = append(byEpisode[row.UnmatchedStopEpisodeID], row)
	}
	out := make(map[int64]any, len(ids))
	for _, id := range ids {
		out[id], _ = stopStaticContextList(id, byEpisode[id])
	}
	return out, nil
}

// listUnmatchedStopEpisodeStaticContexts serves GET /v1/unmatched-stop-episodes/:episode_id/static-contexts.
func (s *InvestigationService) listUnmatchedStopEpisodeStaticContexts(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "episode_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, stopStaticContextIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body stopStaticContextsBody
	var closedAt *time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		episode, err := q.GetUnmatchedStopEpisode(ctx, id)
		if err != nil {
			return err
		}
		closedAt = episode.ClosedAt
		rows, err := q.ListUnmatchedStopEpisodeStaticContexts(ctx, []int64{id})
		if err != nil {
			return err
		}
		var related []itemLinks
		body.stopStaticContexts, related = stopStaticContextList(id, rows)
		body.Included, err = s.loadIncluded(ctx, q, related, inc)
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
