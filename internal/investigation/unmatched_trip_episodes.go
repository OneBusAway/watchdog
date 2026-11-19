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
