package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindGTFSStaticVersion is the include kind of a GTFS static version (E35).
const kindGTFSStaticVersion resourceKind = "gtfs_static_version"

// gtfsStaticVersionSource is one source file entry in an E35 response.
type gtfsStaticVersionSource struct {
	SourceID            int64  `json:"source_id"`
	NormalizedSourceURL string `json:"normalized_source_url"`
	SourceContentSha256 string `json:"source_content_sha256"`
}

// gtfsStaticVersionAgency is one agency entry in an E35 response.
type gtfsStaticVersionAgency struct {
	AgencyID         int64  `json:"agency_id"`
	ExternalAgencyID string `json:"external_agency_id"`
}

// gtfsStaticVersion is one GTFS static version in a response (E35).
type gtfsStaticVersion struct {
	GtfsStaticVersionID   int64                     `json:"gtfs_static_version_id"`
	ServerID              int64                     `json:"server_id"`
	PublishedAt           apiTime                   `json:"published_at"`
	CombinedContentSha256 *string                   `json:"combined_content_sha256"`
	ParserVersion         *string                   `json:"parser_version"`
	Sources               []gtfsStaticVersionSource `json:"sources"`
	Agencies              []gtfsStaticVersionAgency `json:"agencies"`
	Included              included                  `json:"included,omitzero"`
	Links                 links                     `json:"links"`
}

// getGTFSStaticVersion serves GET /v1/gtfs-static-versions/:gtfs_static_version_id:
// one GTFS static version with its sources and agencies.
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

	var body gtfsStaticVersion
	var publishedAt time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetGTFSStaticVersion(ctx, id)
		if err != nil {
			return err
		}
		publishedAt = row.PublishedAt
		sources, err := q.ListGTFSStaticVersionSources(ctx, id)
		if err != nil {
			return err
		}
		agencies, err := q.ListGTFSStaticVersionAgencies(ctx, id)
		if err != nil {
			return err
		}
		l := gtfsStaticVersionLinks(row)
		body = gtfsStaticVersionRepresentation(row, sources, agencies, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cache := cacheImmutable(publishedAt)
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}

// gtfsStaticVersionIncludable lists what include= accepts on E35.
var gtfsStaticVersionIncludable = map[string]resourceKind{"server": kindServer}

// gtfsStaticVersionLinks returns the item links for one version row.
func gtfsStaticVersionLinks(row dbgen.GtfsStaticVersion) itemLinks {
	id := strconv.FormatInt(row.GtfsStaticVersionID, 10)
	return itemLinks{
		"self": {
			Kind: kindGTFSStaticVersion,
			ID:   row.GtfsStaticVersionID,
			URL:  "/v1/gtfs-static-versions/" + id,
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
	}
}

// gtfsStaticVersionRepresentation builds the E35 body from its constituent rows.
func gtfsStaticVersionRepresentation(
	row dbgen.GtfsStaticVersion,
	sources []dbgen.ListGTFSStaticVersionSourcesRow,
	agencies []dbgen.ListGTFSStaticVersionAgenciesRow,
	l itemLinks,
) gtfsStaticVersion {
	srcs := make([]gtfsStaticVersionSource, 0, len(sources))
	for _, s := range sources {
		srcs = append(srcs, gtfsStaticVersionSource{
			SourceID:            s.SourceID,
			NormalizedSourceURL: s.NormalizedSourceUrl,
			SourceContentSha256: s.SourceContentSha256,
		})
	}
	ages := make([]gtfsStaticVersionAgency, 0, len(agencies))
	for _, a := range agencies {
		ages = append(ages, gtfsStaticVersionAgency{
			AgencyID:         a.AgencyID,
			ExternalAgencyID: a.ExternalAgencyID,
		})
	}
	return gtfsStaticVersion{
		GtfsStaticVersionID:   row.GtfsStaticVersionID,
		ServerID:              row.ServerID,
		PublishedAt:           newAPITime(row.PublishedAt),
		CombinedContentSha256: row.CombinedContentSha256,
		ParserVersion:         row.ParserVersion,
		Sources:               srcs,
		Agencies:              ages,
		Links:                 l.links(),
	}
}

// loadGTFSStaticVersions is the include loader for kindGTFSStaticVersion.
func (s *InvestigationService) loadGTFSStaticVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListGTFSStaticVersionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	sourceRows, err := q.ListGTFSStaticVersionSourcesByVersionIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	agencyRows, err := q.ListGTFSStaticVersionAgenciesByVersionIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	// Group sources and agencies by version ID.
	sourcesBy := make(map[int64][]dbgen.ListGTFSStaticVersionSourcesRow, len(rows))
	for _, sr := range sourceRows {
		sourcesBy[sr.GtfsStaticVersionID] = append(sourcesBy[sr.GtfsStaticVersionID],
			dbgen.ListGTFSStaticVersionSourcesRow{
				SourceID:            sr.SourceID,
				NormalizedSourceUrl: sr.NormalizedSourceUrl,
				SourceContentSha256: sr.SourceContentSha256,
			})
	}
	agenciesBy := make(map[int64][]dbgen.ListGTFSStaticVersionAgenciesRow, len(rows))
	for _, ar := range agencyRows {
		agenciesBy[ar.GtfsStaticVersionID] = append(agenciesBy[ar.GtfsStaticVersionID],
			dbgen.ListGTFSStaticVersionAgenciesRow{
				AgencyID:         ar.AgencyID,
				ExternalAgencyID: ar.ExternalAgencyID,
			})
	}

	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		l := gtfsStaticVersionLinks(row)
		rep := gtfsStaticVersionRepresentation(row, sourcesBy[row.GtfsStaticVersionID], agenciesBy[row.GtfsStaticVersionID], l)
		out[row.GtfsStaticVersionID] = rep
	}
	return out, nil
}
