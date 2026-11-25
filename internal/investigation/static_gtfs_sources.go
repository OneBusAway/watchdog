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

// staticGTFSSourceVersionIncludable lists the links E37 can expand.
var staticGTFSSourceVersionIncludable = map[string]resourceKind{"gtfs_static_version": kindGTFSStaticVersion}

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

// staticGTFSSourceVersion is E37's representation of a source in one published version.
type staticGTFSSourceVersion struct {
	GTFSStaticVersionID int64   `json:"gtfs_static_version_id"`
	PublishedAt         apiTime `json:"published_at"`
	SourceContentSHA256 string  `json:"source_content_sha256"`
	Links               links   `json:"links"`
}

// staticGTFSSourceVersionsBody holds one page of E37 results and any included versions.
type staticGTFSSourceVersionsBody struct {
	Items    []staticGTFSSourceVersion `json:"items"`
	Links    links                     `json:"links"`
	Included included                  `json:"included,omitzero"`
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

// listStaticGTFSSourceVersions serves GET /v1/static-gtfs-sources/:source_id/versions (E37).
func (s *InvestigationService) listStaticGTFSSourceVersions(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "source_id")
	if !ok {
		writeNotFound(w)
		return
	}

	var bad invalidParams
	values := parseQuery(r, &bad, "page_size", "cursor", "include")
	p := parsePage(values, &bad)
	inc := s.parseInclude(values, staticGTFSSourceVersionIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body staticGTFSSourceVersionsBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		body, err = s.staticGTFSSourceVersionPage(ctx, q, r, id, p, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// staticGTFSSourceVersionPage checks that the source exists and loads one page
// of its version history and any included versions in the same transaction.
func (s *InvestigationService) staticGTFSSourceVersionPage(ctx context.Context, q *dbgen.Queries, r *http.Request,
	sourceID int64, p page, inc include) (staticGTFSSourceVersionsBody, error) {
	var body staticGTFSSourceVersionsBody
	// An existing source can have an empty page, so check existence separately.
	if _, err := q.GetStaticGTFSSource(ctx, sourceID); err != nil {
		return body, err
	}
	rows, err := q.ListStaticGTFSSourceVersions(ctx, dbgen.ListStaticGTFSSourceVersionsParams{
		SourceID:  sourceID,
		AfterID:   p.AfterID,
		PageLimit: p.Limit(),
	})
	if err != nil {
		return body, err
	}
	rows, hasMore := trimPage(rows, p)
	body.Items = make([]staticGTFSSourceVersion, 0, len(rows))
	allLinks := make([]itemLinks, 0, len(rows))
	var lastID int64
	for _, row := range rows {
		id := row.GtfsStaticVersionID
		l := itemLinks{
			"gtfs_static_version": {
				Kind: kindGTFSStaticVersion,
				ID:   id,
				URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(id, 10),
			},
		}
		body.Items = append(body.Items, staticGTFSSourceVersionRepresentation(id, row.PublishedAt, row.SourceContentSha256, l))
		allLinks = append(allLinks, l)
		lastID = id
	}
	body.Links = pageLinks(r, lastID, hasMore)
	body.Included, err = s.loadIncluded(ctx, q, allLinks, inc)
	return body, err
}

// staticGTFSSourceVersionRepresentation maps one version's source hash, publication time, and links to E37.
func staticGTFSSourceVersionRepresentation(versionID int64, publishedAt time.Time, hash string, l itemLinks) staticGTFSSourceVersion {
	return staticGTFSSourceVersion{
		GTFSStaticVersionID: versionID,
		PublishedAt:         newAPITime(publishedAt),
		SourceContentSHA256: hash,
		Links:               l.links(),
	}
}
