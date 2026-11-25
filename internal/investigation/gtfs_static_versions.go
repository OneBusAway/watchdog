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

// kindGTFSStaticVersion is the include kind of a static GTFS version (E35).
// Every link named gtfs_static_version points to one.
const kindGTFSStaticVersion resourceKind = "gtfs_static_version"

// gtfsStaticVersionIncludable names the links E34 and E35 can include.
var gtfsStaticVersionIncludable = map[string]resourceKind{"server": kindServer}

type gtfsStaticVersionListItem struct {
	GTFSStaticVersionID   int64   `json:"gtfs_static_version_id"`
	ServerID              int64   `json:"server_id"`
	PublishedAt           apiTime `json:"published_at"`
	CombinedContentSHA256 *string `json:"combined_content_sha256"`
	ParserVersion         *string `json:"parser_version"`
	Links                 links   `json:"links"`
}

type gtfsStaticVersionsBody struct {
	Items    []gtfsStaticVersionListItem `json:"items"`
	Links    links                       `json:"links"`
	Included included                    `json:"included,omitzero"`
}

// gtfsStaticVersionSource is one source file a static version was built from,
// with that file's content hash in this version.
type gtfsStaticVersionSource struct {
	SourceID            int64  `json:"source_id"`
	NormalizedSourceURL string `json:"normalized_source_url"`
	SourceContentSHA256 string `json:"source_content_sha256"`
}

// gtfsStaticVersionAgency is one agency a static version covered.
type gtfsStaticVersionAgency struct {
	AgencyID         int64  `json:"agency_id"`
	ExternalAgencyID string `json:"external_agency_id"`
}

// gtfsStaticVersion is the E35 representation of one static version, which
// the gtfs_static_version loader also returns.
type gtfsStaticVersion struct {
	GTFSStaticVersionID   int64                     `json:"gtfs_static_version_id"`
	ServerID              int64                     `json:"server_id"`
	PublishedAt           apiTime                   `json:"published_at"`
	CombinedContentSHA256 *string                   `json:"combined_content_sha256"`
	ParserVersion         *string                   `json:"parser_version"`
	Sources               []gtfsStaticVersionSource `json:"sources"`
	Agencies              []gtfsStaticVersionAgency `json:"agencies"`
	Links                 links                     `json:"links"`
}

// gtfsStaticVersionBody is the E35 response.
type gtfsStaticVersionBody struct {
	gtfsStaticVersion
	Included included `json:"included,omitzero"`
}

// gtfsStaticVersionLinks builds a version's links. Both always exist: a
// version's server is a foreign key.
func gtfsStaticVersionLinks(versionID, serverID int64) itemLinks {
	return itemLinks{
		"self": {URL: "/v1/gtfs-static-versions/" + strconv.FormatInt(versionID, 10)},
		"server": {
			Kind: kindServer,
			ID:   serverID,
			URL:  "/v1/servers/" + strconv.FormatInt(serverID, 10),
		},
	}
}

func gtfsStaticVersionListRepresentation(row dbgen.GtfsStaticVersion, l itemLinks) gtfsStaticVersionListItem {
	return gtfsStaticVersionListItem{
		GTFSStaticVersionID:   row.GtfsStaticVersionID,
		ServerID:              row.ServerID,
		PublishedAt:           newAPITime(row.PublishedAt),
		CombinedContentSHA256: row.CombinedContentSha256,
		ParserVersion:         row.ParserVersion,
		Links:                 l.links(),
	}
}

// listGTFSStaticVersions serves GET /v1/gtfs-static-versions.
func (s *InvestigationService) listGTFSStaticVersions(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "server_id", "published_from", "published_to", "page_size", "cursor", "include")
	serverID := paramID(values, "server_id", &bad)
	win := s.parseWindow(values, unboundedWindowOptions("published_from", "published_to"), &bad)
	p := parsePage(values, &bad)
	inc := s.parseInclude(values, gtfsStaticVersionIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	params := dbgen.ListGTFSStaticVersionsParams{
		ServerID:      serverID,
		PublishedFrom: win.From,
		PublishedTo:   win.To,
		AfterID:       p.AfterID,
		PageLimit:     p.Limit(),
	}
	var body gtfsStaticVersionsBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListGTFSStaticVersions(ctx, params)
		if err != nil {
			return err
		}
		rows, hasMore := trimPage(rows, p)
		body.Items = make([]gtfsStaticVersionListItem, 0, len(rows))
		allLinks := make([]itemLinks, 0, len(rows))
		var lastID int64
		for _, row := range rows {
			l := gtfsStaticVersionLinks(row.GtfsStaticVersionID, row.ServerID)
			body.Items = append(body.Items, gtfsStaticVersionListRepresentation(row, l))
			allLinks = append(allLinks, l)
			lastID = row.GtfsStaticVersionID
		}
		body.Links = pageLinks(r, lastID, hasMore)
		body.Included, err = s.loadIncluded(ctx, q, allLinks, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// gtfsStaticVersionRepresentation maps one version row and its source and
// agency rows to E35. sources must be ordered by source_id and agencies by
// agency_id, as the queries return them. Neither array is ever nil, so a
// version with no rows of either kind encodes [] rather than null.
func gtfsStaticVersionRepresentation(row dbgen.GtfsStaticVersion, sources []dbgen.ListGTFSStaticVersionSourcesRow, agencies []dbgen.ListGTFSStaticVersionAgenciesRow) gtfsStaticVersion {
	rep := gtfsStaticVersion{
		GTFSStaticVersionID:   row.GtfsStaticVersionID,
		ServerID:              row.ServerID,
		PublishedAt:           newAPITime(row.PublishedAt),
		CombinedContentSHA256: row.CombinedContentSha256,
		ParserVersion:         row.ParserVersion,
		Sources:               make([]gtfsStaticVersionSource, 0, len(sources)),
		Agencies:              make([]gtfsStaticVersionAgency, 0, len(agencies)),
		Links:                 gtfsStaticVersionLinks(row.GtfsStaticVersionID, row.ServerID).links(),
	}
	for _, s := range sources {
		rep.Sources = append(rep.Sources, gtfsStaticVersionSource{
			SourceID:            s.SourceID,
			NormalizedSourceURL: s.NormalizedSourceUrl,
			SourceContentSHA256: s.SourceContentSha256,
		})
	}
	for _, a := range agencies {
		rep.Agencies = append(rep.Agencies, gtfsStaticVersionAgency{
			AgencyID:         a.AgencyID,
			ExternalAgencyID: a.ExternalAgencyID,
		})
	}
	return rep
}

// readGTFSStaticVersions returns the E35 representation of each version in
// ids that exists, by ID. It runs three queries however many IDs it is given
// (versions, then their sources, then their agencies), or only the first when
// none of the IDs exists. E35 and its include loader both read through it, so
// a client sees the same thing whether it follows a link or reads included.
func readGTFSStaticVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]gtfsStaticVersion, error) {
	versions, err := q.GetGTFSStaticVersion(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]gtfsStaticVersion, len(versions))
	if len(versions) == 0 {
		return out, nil
	}
	sourceRows, err := q.ListGTFSStaticVersionSources(ctx, ids)
	if err != nil {
		return nil, err
	}
	agencyRows, err := q.ListGTFSStaticVersionAgencies(ctx, ids)
	if err != nil {
		return nil, err
	}

	sources := make(map[int64][]dbgen.ListGTFSStaticVersionSourcesRow, len(versions))
	for _, row := range sourceRows {
		sources[row.GtfsStaticVersionID] = append(sources[row.GtfsStaticVersionID], row)
	}
	agencies := make(map[int64][]dbgen.ListGTFSStaticVersionAgenciesRow, len(versions))
	for _, row := range agencyRows {
		agencies[row.GtfsStaticVersionID] = append(agencies[row.GtfsStaticVersionID], row)
	}
	for _, row := range versions {
		id := row.GtfsStaticVersionID
		out[id] = gtfsStaticVersionRepresentation(row, sources[id], agencies[id])
	}
	return out, nil
}

// getGTFSStaticVersion serves GET /v1/gtfs-static-versions/:gtfs_static_version_id:
// one static version, with the source files it was built from and the
// agencies it covered. A path ID that is not a positive integer is a 404
// rather than a 400, because no such version can exist.
func (s *InvestigationService) getGTFSStaticVersion(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "gtfs_static_version_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, gtfsStaticVersionIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body gtfsStaticVersionBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		versions, err := readGTFSStaticVersions(ctx, q, []int64{id})
		if err != nil {
			return err
		}
		version, ok := versions[id]
		if !ok {
			return pgx.ErrNoRows
		}
		body.gtfsStaticVersion = version
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{gtfsStaticVersionLinks(id, version.ServerID)}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A static version, its sources and its agencies never change once
	// published, so the version's own time is its Last-Modified. An included
	// server can change (its configured flag), so a request with include gets
	// the short policy, as the sibling detail handlers do.
	cache := cacheImmutable(time.Time(body.PublishedAt))
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}

// loadGTFSStaticVersions is the batched loader behind include=gtfs_static_version
// on every endpoint that links here. It runs E35's three queries for all the
// requested IDs together and returns exactly E35's representation for each.
func loadGTFSStaticVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	versions, err := readGTFSStaticVersions(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(versions))
	for id, version := range versions {
		out[id] = version
	}
	return out, nil
}
