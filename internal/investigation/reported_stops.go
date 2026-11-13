package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindReportedStop is the include kind of a reported stop (E09).
const kindReportedStop resourceKind = "reported_stop"

// reportedStop is one reported stop in a response (E08, E09).
type reportedStop struct {
	ReportedStopID int64  `json:"reported_stop_id"`
	AgencyID       int64  `json:"agency_id"`
	StopID         string `json:"stop_id"`
	Links          links  `json:"links"`
}

// reportedStopList is the E08 body. It is not paginated.
type reportedStopList struct {
	Items    []reportedStop `json:"items"`
	Links    links          `json:"links"`
	Included included       `json:"included,omitzero"`
}

// reportedStopBody is the E09 body.
type reportedStopBody struct {
	reportedStop
	Included included `json:"included,omitzero"`
}

// reportedStopIncludable is what include accepts on E08 and E09.
var reportedStopIncludable = map[string]resourceKind{"agency": kindAgency}

// listReportedStops serves GET /v1/reported-stops.
func (s *InvestigationService) listReportedStops(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "agency_id", "stop_id", "include")
	required(values, &bad, "agency_id", "stop_id")
	agencyID := paramID(values, "agency_id", &bad)
	stopID := paramString(values, "stop_id", &bad)
	inc := s.parseInclude(values, reportedStopIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body reportedStopList
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListReportedStops(ctx, dbgen.ListReportedStopsParams{
			AgencyID:  *agencyID,
			ObaStopID: *stopID,
		})
		if err != nil {
			return err
		}
		items, related := reportedStopItems(rows)
		body = reportedStopList{
			Items: items,
			Links: links{"self": linkWithQuery(r.URL.Path, r.URL.Query())},
		}
		body.Included, err = s.loadIncluded(ctx, q, related, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// reportedStopItems maps rows to their representations and links for loadIncluded.
func reportedStopItems(rows []dbgen.ReportedStop) ([]reportedStop, []itemLinks) {
	items := make([]reportedStop, 0, len(rows))
	related := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := reportedStopLinks(row)
		items = append(items, reportedStopRepresentation(row, l))
		related = append(related, l)
	}
	return items, related
}

// reportedStopLinks is everything one reported stop links to.
func reportedStopLinks(row dbgen.ReportedStop) itemLinks {
	self := "/v1/reported-stops/" + strconv.FormatInt(row.ReportedStopID, 10)
	return itemLinks{
		"self": {
			Kind: kindReportedStop,
			ID:   row.ReportedStopID,
			URL:  self,
		},
		"agency": {
			Kind: kindAgency,
			ID:   row.AgencyID,
			URL:  "/v1/agencies/" + strconv.FormatInt(row.AgencyID, 10),
		},
		"unmatched_stop_episodes": {URL: self + "/unmatched-stop-episodes"},
	}
}

// reportedStopRepresentation maps one row to its E08 and E09 representation.
func reportedStopRepresentation(row dbgen.ReportedStop, l itemLinks) reportedStop {
	return reportedStop{
		ReportedStopID: row.ReportedStopID,
		AgencyID:       row.AgencyID,
		StopID:         row.ObaStopID,
		Links:          l.links(),
	}
}

// getReportedStop serves GET /v1/reported-stops/:reported_stop_id.
func (s *InvestigationService) getReportedStop(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "reported_stop_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, reportedStopIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body reportedStopBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetReportedStop(ctx, id)
		if err != nil {
			return err
		}
		l := reportedStopLinks(row)
		body.reportedStop = reportedStopRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheImmutable(time.Time{}))
}

// loadReportedStops is the include loader for kindReportedStop.
func loadReportedStops(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListReportedStopsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[row.ReportedStopID] = reportedStopRepresentation(row, reportedStopLinks(row))
	}
	return out, nil
}
