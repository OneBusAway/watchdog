package investigation

import (
	"context"
	"net/http"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindAgency is the include kind of an agency (E05). Every link named agency
// points to one.
const kindAgency resourceKind = "agency"

// agency is one agency in a response (E04, E05).
type agency struct {
	AgencyID         int64   `json:"agency_id"`
	ServerID         int64   `json:"server_id"`
	ExternalAgencyID string  `json:"external_agency_id"`
	CurrentName      *string `json:"current_name"`
	Links            links   `json:"links"`
}

// agencyList is the E04 body. It is not paginated: a server has few agencies.
type agencyList struct {
	Items    []agency `json:"items"`
	Links    links    `json:"links"`
	Included included `json:"included,omitzero"`
}

// serverAgenciesIncludable is what E04's include accepts.
var serverAgenciesIncludable = map[string]resourceKind{"server": kindServer}

// listServerAgencies serves GET /v1/servers/:server_id/agencies: the agencies
// recorded for one server, by agency_id ascending. ?external_agency_id= finds
// the one agency with that OBA agency ID, which is how a client turns an
// agency ID (only unique within a server) into the agency_id other endpoints
// need. An unknown server is a 404, not an empty list.
func (s *InvestigationService) listServerAgencies(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	serverID, ok := pathID(ps, "server_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "external_agency_id", "include")
	externalID := paramString(values, "external_agency_id", &bad)
	inc := s.parseInclude(values, serverAgenciesIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body agencyList
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		// GetServer returns pgx.ErrNoRows for an unknown server, which fail
		// turns into a 404.
		if _, err := q.GetServer(ctx, serverID); err != nil {
			return err
		}
		rows, err := q.ListServerAgencies(ctx, dbgen.ListServerAgenciesParams{
			ServerID:         serverID,
			ExternalAgencyID: externalID,
		})
		if err != nil {
			return err
		}
		items, related := agencyItems(rows)
		body = agencyList{
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

// agencyItems maps rows to their representations, and returns each item's
// links for loadIncluded. The items are never nil, so an empty list encodes
// as [] rather than null.
func agencyItems(rows []dbgen.Agency) ([]agency, []itemLinks) {
	items := make([]agency, 0, len(rows))
	related := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := agencyLinks(row)
		items = append(items, agencyRepresentation(row, l))
		related = append(related, l)
	}
	return items, related
}

// agencyLinks is everything one agency links to.
func agencyLinks(row dbgen.Agency) itemLinks {
	return itemLinks{
		"self": {
			Kind: kindAgency,
			ID:   row.AgencyID,
			URL:  "/v1/agencies/" + strconv.FormatInt(row.AgencyID, 10),
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
	}
}

// agencyRepresentation maps one row to the representation that E04 lists and
// E05 returns.
func agencyRepresentation(row dbgen.Agency, l itemLinks) agency {
	return agency{
		AgencyID:         row.AgencyID,
		ServerID:         row.ServerID,
		ExternalAgencyID: row.ExternalAgencyID,
		CurrentName:      row.CurrentName,
		Links:            l.links(),
	}
}
