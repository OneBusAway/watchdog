package investigation

import (
	"context"
	"net/http"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindStaticGtfsSource is the include kind of a static GTFS source (E36).
const kindStaticGtfsSource resourceKind = "static_gtfs_source"

// staticGtfsSource is one static GTFS source in a response (E36).
type staticGtfsSource struct {
	SourceID            int64  `json:"source_id"`
	ServerID            int64  `json:"server_id"`
	NormalizedSourceURL string `json:"normalized_source_url"`
	Links               links  `json:"links"`
}

// staticGtfsSourceIncludable is what include accepts on E36: a source links
// to one server.
var staticGtfsSourceIncludable = map[string]resourceKind{"server": kindServer}

// staticGtfsSourceBody is the E36 body: the source, plus included when asked.
type staticGtfsSourceBody struct {
	staticGtfsSource
	Included included `json:"included,omitzero"`
}

// getStaticGtfsSource serves GET /v1/static-gtfs-sources/:source_id (E36).
func (s *InvestigationService) getStaticGtfsSource(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "source_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, staticGtfsSourceIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body staticGtfsSourceBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetStaticGtfsSource(ctx, id)
		if err != nil {
			return err
		}
		l := staticGtfsSourceLinks(row)
		body.staticGtfsSource = staticGtfsSourceRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// staticGtfsSourceLinks is everything one static GTFS source links to that
// can be included or is included elsewhere: itself and its server.
func staticGtfsSourceLinks(row dbgen.StaticGtfsSource) itemLinks {
	return itemLinks{
		"self": {
			Kind: kindStaticGtfsSource,
			ID:   row.SourceID,
			URL:  "/v1/static-gtfs-sources/" + strconv.FormatInt(row.SourceID, 10),
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
	}
}

// staticGtfsSourceRepresentation maps one row to its E36 representation.
func staticGtfsSourceRepresentation(row dbgen.StaticGtfsSource, l itemLinks) staticGtfsSource {
	return staticGtfsSource{
		SourceID:            row.SourceID,
		ServerID:            row.ServerID,
		NormalizedSourceURL: row.NormalizedSourceUrl,
		Links:               l.links(),
	}
}

// loadStaticGtfsSources is the include loader for kindStaticGtfsSource.
func loadStaticGtfsSources(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListStaticGtfsSourcesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[row.SourceID] = staticGtfsSourceRepresentation(row, staticGtfsSourceLinks(row))
	}
	return out, nil
}
