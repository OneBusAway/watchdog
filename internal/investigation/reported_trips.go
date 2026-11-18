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

// kindReportedTrip is the include kind of a reported trip (E12).
const kindReportedTrip resourceKind = "reported_trip"

// reportedTripIncludable names the to-one links this resource can include.
// agency resolves through kindAgency (registered next to getAgency).
var reportedTripIncludable = map[string]resourceKind{"agency": kindAgency}

type reportedTrip struct {
	ReportedTripID int64  `json:"reported_trip_id"`
	AgencyID       int64  `json:"agency_id"`
	TripID         string `json:"trip_id"`
	Links          links  `json:"links"`
}

type reportedTripsBody struct {
	Items    []reportedTrip `json:"items"`
	Links    links          `json:"links"`
	Included included       `json:"included,omitzero"`
}

// reportedTripBody is the E12 response.
type reportedTripBody struct {
	reportedTrip
	Included included `json:"included,omitzero"`
}

func reportedTripLinks(row dbgen.ReportedTrip) itemLinks {
	self := "/v1/reported-trips/" + strconv.FormatInt(row.ReportedTripID, 10)
	return itemLinks{
		"self":                    {URL: self},
		"agency":                  {Kind: kindAgency, ID: row.AgencyID, URL: "/v1/agencies/" + strconv.FormatInt(row.AgencyID, 10)},
		"unmatched_trip_episodes": {URL: self + "/unmatched-trip-episodes"},
	}
}

func reportedTripRepresentation(row dbgen.ReportedTrip, l itemLinks) reportedTrip {
	return reportedTrip{ReportedTripID: row.ReportedTripID, AgencyID: row.AgencyID, TripID: row.ObaTripID, Links: l.links()}
}

// listReportedTrips looks up an exact OBA trip ID within its required agency.
// The identity's unique constraint guarantees zero or one result.
func (s *InvestigationService) listReportedTrips(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "agency_id", "trip_id", "include")
	agencyID := paramID(values, "agency_id", &bad)
	tripID := paramString(values, "trip_id", &bad)
	for _, name := range []string{"agency_id", "trip_id"} {
		if !values.Has(name) {
			bad.add(name, "is required")
		}
	}
	inc := s.parseInclude(values, reportedTripIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body reportedTripsBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListReportedTrips(ctx, dbgen.ListReportedTripsParams{AgencyID: *agencyID, ObaTripID: *tripID})
		if err != nil {
			return err
		}
		body.Items = make([]reportedTrip, 0, len(rows))
		all := make([]itemLinks, 0, len(rows))
		for _, row := range rows {
			l := reportedTripLinks(row)
			body.Items = append(body.Items, reportedTripRepresentation(row, l))
			all = append(all, l)
		}
		body.Links = links{"self": linkWithQuery(r.URL.Path, r.URL.Query())}
		body.Included, err = s.loadIncluded(ctx, q, all, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// getReportedTrip serves GET /v1/reported-trips/:reported_trip_id: one reported
// trip identity. A path ID that is not a positive integer is a 404 rather than
// a 400, because no such trip can exist.
func (s *InvestigationService) getReportedTrip(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "reported_trip_id")
	if !ok {
		s.fail(w, r, pgx.ErrNoRows)
		return
	}

	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, reportedTripIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body reportedTripBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetReportedTrip(ctx, id)
		if err != nil {
			return err
		}
		l := reportedTripLinks(row)
		body.reportedTrip = reportedTripRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A reported-trip identity is immutable once recorded, but included
	// resources (agency.current_name) can change. The identity has no
	// Last-Modified because its columns are never updated.
	cache := cacheShort()
	if !inc.requested() {
		cache = cacheImmutable(time.Time{})
	}
	s.writeJSON(w, r, body, cache)
}

// loadReportedTrips is the batched loader behind include=reported_trip on the
// endpoints that link here. It reads every requested ID in one query and
// returns exactly this endpoint's representation for each.
func loadReportedTrips(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListReportedTripsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	reps := make(map[int64]any, len(rows))
	for _, row := range rows {
		reps[row.ReportedTripID] = reportedTripRepresentation(row, reportedTripLinks(row))
	}
	return reps, nil
}
