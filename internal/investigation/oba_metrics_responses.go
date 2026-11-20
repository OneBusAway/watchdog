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

// obaMetricsResponseIncludable accepts server once its loader is registered (#217).
var obaMetricsResponseIncludable = map[string]resourceKind{
	"server": kindServer,
}

// obaMetricsAgency is one agency's counts within an OBA metrics response (E20).
type obaMetricsAgency struct {
	AgencyID           int64  `json:"agency_id"`
	ResponseComplete   bool   `json:"response_complete"`
	UnmatchedStopCount *int32 `json:"unmatched_stop_count"`
	UnmatchedTripCount *int32 `json:"unmatched_trip_count"`
	MatchedStopCount   *int32 `json:"matched_stop_count"`
	MatchedTripCount   *int32 `json:"matched_trip_count"`
}

// obaMetricsResponse is the E20 representation of one OBA metrics response.
type obaMetricsResponse struct {
	ResponseID          int64              `json:"response_id"`
	ServerID            int64              `json:"server_id"`
	ObservedAt          apiTime            `json:"observed_at"`
	UpstreamCurrentTime *apiTime           `json:"upstream_current_time"`
	APIVersion          *int32             `json:"api_version"`
	Agencies            []obaMetricsAgency `json:"agencies"`
	Links               links              `json:"links"`
}

// obaMetricsResponseBody is the E20 response.
type obaMetricsResponseBody struct {
	obaMetricsResponse
	Included included `json:"included,omitzero"`
}

// obaMetricsResponseLinks builds a response's links; the server link always exists.
func obaMetricsResponseLinks(row dbgen.OnebusawayMetricsResponse) itemLinks {
	return itemLinks{
		"self": {URL: "/v1/oba-metrics-responses/" + strconv.FormatInt(row.ResponseID, 10)},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
	}
}

// obaMetricsResponseRepresentation maps one response header and its agency rows to E20.
// agencies must already be ordered by agency_id.
func obaMetricsResponseRepresentation(row dbgen.OnebusawayMetricsResponse, agencies []dbgen.ListOBAMetricsResponseAgenciesRow, l itemLinks) obaMetricsResponse {
	rep := obaMetricsResponse{
		ResponseID:          row.ResponseID,
		ServerID:            row.ServerID,
		ObservedAt:          newAPITime(row.ObservedAt),
		UpstreamCurrentTime: newOptionalAPITime(row.UpstreamCurrentTime),
		APIVersion:          row.ApiVersion,
		Agencies:            make([]obaMetricsAgency, 0, len(agencies)),
		Links:               l.links(),
	}
	for _, a := range agencies {
		rep.Agencies = append(rep.Agencies, obaMetricsAgency{
			AgencyID:           a.AgencyID,
			ResponseComplete:   a.ResponseComplete,
			UnmatchedStopCount: a.UnmatchedStopCount,
			UnmatchedTripCount: a.UnmatchedTripCount,
			MatchedStopCount:   a.MatchedStopCount,
			MatchedTripCount:   a.MatchedTripCount,
		})
	}
	return rep
}

// loadOBAMetricsResponses is the batched include loader for kindOBAMetricsResponse.
// One query reads every requested response's header and agency rows whatever the
// number of IDs, and the rows are grouped by response ID. A response with no
// agency rows still gets an entry, with an empty agencies array.
func loadOBAMetricsResponses(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListOBAMetricsResponsesWithAgencies(ctx, ids)
	if err != nil {
		return nil, err
	}
	headers := make(map[int64]dbgen.OnebusawayMetricsResponse, len(ids))
	byResponse := make(map[int64][]dbgen.ListOBAMetricsResponseAgenciesRow, len(ids))
	for _, row := range rows {
		if _, seen := headers[row.ResponseID]; !seen {
			headers[row.ResponseID] = dbgen.OnebusawayMetricsResponse{
				ResponseID:          row.ResponseID,
				ServerID:            row.ServerID,
				ObservedAt:          row.ObservedAt,
				UpstreamCurrentTime: row.UpstreamCurrentTime,
				ApiVersion:          row.ApiVersion,
			}
		}
		// The LEFT JOIN yields NULL agency columns for a response with no agency rows.
		// A matched agency row always has all of them set, since they are NOT NULL in the schema.
		if row.AgencyID != nil {
			byResponse[row.ResponseID] = append(byResponse[row.ResponseID], dbgen.ListOBAMetricsResponseAgenciesRow{
				ResponseID:         row.ResponseID,
				AgencyID:           *row.AgencyID,
				ResponseComplete:   *row.ResponseComplete,
				UnmatchedStopCount: row.UnmatchedStopCount,
				UnmatchedTripCount: row.UnmatchedTripCount,
				MatchedStopCount:   row.MatchedStopCount,
				MatchedTripCount:   row.MatchedTripCount,
			})
		}
	}
	out := make(map[int64]any, len(headers))
	for id, header := range headers {
		out[id] = obaMetricsResponseRepresentation(header, byResponse[id], obaMetricsResponseLinks(header))
	}
	return out, nil
}

// getOBAMetricsResponse serves GET /v1/oba-metrics-responses/:response_id.
func (s *InvestigationService) getOBAMetricsResponse(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "response_id")
	if !ok {
		s.fail(w, r, pgx.ErrNoRows)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, obaMetricsResponseIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body obaMetricsResponseBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetOBAMetricsResponse(ctx, id)
		if err != nil {
			return err
		}
		agencyRows, err := q.ListOBAMetricsResponseAgencies(ctx, []int64{id})
		if err != nil {
			return err
		}
		l := obaMetricsResponseLinks(row)
		body.obaMetricsResponse = obaMetricsResponseRepresentation(row, agencyRows, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The issue specifies the immutable policy with no Last-Modified, even when
	// include is requested.
	s.writeJSON(w, r, body, cacheImmutable(time.Time{}))
}
