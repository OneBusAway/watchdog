package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// staticGTFSSourceIncludable lists the links E36 can expand. The server
// loader is registered by the server endpoints in registerRoutes.
var staticGTFSSourceIncludable = map[string]resourceKind{"server": kindServer}

// staticGTFSSource is the E36 representation of one static GTFS source URL.
type staticGTFSSource struct {
	SourceID            int64  `json:"source_id"`
	ServerID            int64  `json:"server_id"`
	NormalizedSourceURL string `json:"normalized_source_url"`
	Links               links  `json:"links"`
}

// staticGTFSSourceBody adds included server representations when requested.
type staticGTFSSourceBody struct {
	staticGTFSSource
	Included included `json:"included,omitzero"`
}

// staticGTFSSourceLinks builds E36's three absolute-path links.
func staticGTFSSourceLinks(sourceID, serverID int64) itemLinks {
	sourcePath := "/v1/static-gtfs-sources/" + strconv.FormatInt(sourceID, 10)
	return itemLinks{
		"self": {URL: sourcePath},
		"server": {
			Kind: kindServer,
			ID:   serverID,
			URL:  "/v1/servers/" + strconv.FormatInt(serverID, 10),
		},
		"versions": {URL: sourcePath + "/versions"},
	}
}

// staticGTFSSourceRepresentation maps one database row and its links to E36.
func staticGTFSSourceRepresentation(sourceID, serverID int64, normalizedSourceURL string, l itemLinks) staticGTFSSource {
	return staticGTFSSource{
		SourceID:            sourceID,
		ServerID:            serverID,
		NormalizedSourceURL: normalizedSourceURL,
		Links:               l.links(),
	}
}

// getStaticGTFSSource serves GET /v1/static-gtfs-sources/:source_id (E36).
func (s *InvestigationService) getStaticGTFSSource(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "source_id")
	if !ok {
		writeNotFound(w)
		return
	}

	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, staticGTFSSourceIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body staticGTFSSourceBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetStaticGTFSSource(ctx, id)
		if err != nil {
			return err
		}
		l := staticGTFSSourceLinks(row.SourceID, row.ServerID)
		body.staticGTFSSource = staticGTFSSourceRepresentation(
			row.SourceID, row.ServerID, row.NormalizedSourceUrl, l,
		)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The source URL is immutable and has no Last-Modified time. An included
	// resource can change, so use the short cache whenever one is requested.
	cache := cacheImmutable(time.Time{})
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}
