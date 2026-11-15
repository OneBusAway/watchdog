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

const (
	kindOBAMetricsResponse          resourceKind = "oba_metrics_response"
	kindUnmatchedStopStaticContexts resourceKind = "unmatched_stop_static_contexts"
)

// unmatchedStopEpisodeIncludable uses the kinds the agency, server and reported-stop endpoints register.
var unmatchedStopEpisodeIncludable = map[string]resourceKind{
	"reported_stop":    "reported_stop",
	"agency":           "agency",
	"server":           "server",
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
			Kind: "reported_stop",
			ID:   row.ReportedStopID,
			URL:  "/v1/reported-stops/" + strconv.FormatInt(row.ReportedStopID, 10),
		},
		"agency": {
			Kind: "agency",
			ID:   row.AgencyID,
			URL:  "/v1/agencies/" + strconv.FormatInt(row.AgencyID, 10),
		},
		"server": {
			Kind: "server",
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
		"close_reason", "static_found", "include", "page_size", "cursor")
	params := dbgen.ListUnmatchedStopEpisodesParams{
		ServerID:    paramID(values, "server_id", &bad),
		AgencyID:    paramID(values, "agency_id", &bad),
		ObaStopID:   paramString(values, "stop_id", &bad),
		Status:      paramEnum(values, "status", []string{"open", "closed"}, &bad),
		CloseReason: paramEnum(values, "close_reason", []string{"not_reported"}, &bad),
		StaticFound: paramBool(values, "static_found", &bad),
	}
	if params.ObaStopID != nil && !values.Has("agency_id") {
		bad.add("stop_id", "requires agency_id")
	}
	statusOpen := params.Status != nil && *params.Status == "open"
	win := s.parseWindow(values, activeWindowOptions(statusOpen, false), &bad)
	inc := s.parseInclude(values, unmatchedStopEpisodeIncludable, &bad)
	p := parsePage(values, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}
	params.ActiveFrom, params.ActiveTo = win.From, win.To
	params.AfterID, params.PageLimit = p.AfterID, p.Limit()

	var body unmatchedStopEpisodesBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListUnmatchedStopEpisodes(ctx, params)
		if err != nil {
			return err
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
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}
