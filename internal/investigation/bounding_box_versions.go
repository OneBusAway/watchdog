package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindBoundingBoxVersion is the include kind of a bounding-box version (E29).
const kindBoundingBoxVersion resourceKind = "bounding_box_version"

// boundingBoxVersion is one bounding-box version in a response (E29).
type boundingBoxVersion struct {
	BoundingBoxVersionID int64   `json:"bounding_box_version_id"`
	GtfsStaticVersionID  int64   `json:"gtfs_static_version_id"`
	ServerID             int64   `json:"server_id"`
	AgencyID             *int64  `json:"agency_id"`
	MinimumLatitude      float64 `json:"minimum_latitude"`
	MaximumLatitude      float64 `json:"maximum_latitude"`
	MinimumLongitude     float64 `json:"minimum_longitude"`
	MaximumLongitude     float64 `json:"maximum_longitude"`
	Links                links   `json:"links"`
}

// boundingBoxVersionBody is the E29 body: the version, plus included when
// asked for. Embedding keeps the version's fields at the top level.
type boundingBoxVersionBody struct {
	boundingBoxVersion
	Included included `json:"included,omitzero"`
}

// boundingBoxVersionIncludable is what include accepts on E29: a version
// links to one server and optionally one agency.
var boundingBoxVersionIncludable = map[string]resourceKind{
	"server": kindServer,
	"agency": kindAgency,
}

// getBoundingBoxVersion serves GET /v1/bounding-box-versions/:bounding_box_version_id:
// one bounding-box version by its ID. An unknown ID returns 404.
// Bounding-box versions are immutable; responses are cached for a long time.
func (s *InvestigationService) getBoundingBoxVersion(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "bounding_box_version_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, boundingBoxVersionIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body boundingBoxVersionBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetBoundingBoxVersion(ctx, id)
		if err != nil {
			return err
		}
		l := boundingBoxVersionLinks(row)
		body.boundingBoxVersion = boundingBoxVersionRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheImmutable(time.Time{}))
}

// boundingBoxVersionLinks is everything one version links to that can be
// included: itself, its server, and its agency (if any).
func boundingBoxVersionLinks(row dbgen.BoundingBoxVersion) itemLinks {
	self := "/v1/bounding-box-versions/" + strconv.FormatInt(row.BoundingBoxVersionID, 10)
	l := itemLinks{
		"self":   related{Kind: kindBoundingBoxVersion, ID: row.BoundingBoxVersionID, URL: self},
		"server": related{Kind: kindServer, ID: row.ServerID, URL: "/v1/servers/" + strconv.FormatInt(row.ServerID, 10)},
		"gtfs_static_version": related{
			URL: "/v1/gtfs-static-versions/" + strconv.FormatInt(row.GtfsStaticVersionID, 10),
		},
	}
	if row.AgencyID != nil {
		l["agency"] = related{
			Kind: kindAgency,
			ID:   *row.AgencyID,
			URL:  "/v1/agencies/" + strconv.FormatInt(*row.AgencyID, 10),
		}
	}
	return l
}

// boundingBoxVersionRepresentation maps one row to the representation that
// E29 returns.
func boundingBoxVersionRepresentation(row dbgen.BoundingBoxVersion, l itemLinks) boundingBoxVersion {
	return boundingBoxVersion{
		BoundingBoxVersionID: row.BoundingBoxVersionID,
		GtfsStaticVersionID:  row.GtfsStaticVersionID,
		ServerID:             row.ServerID,
		AgencyID:             row.AgencyID,
		MinimumLatitude:      row.MinimumLatitude,
		MaximumLatitude:      row.MaximumLatitude,
		MinimumLongitude:     row.MinimumLongitude,
		MaximumLongitude:     row.MaximumLongitude,
		Links:                l.links(),
	}
}

// loadBoundingBoxVersions is the include loader for kindBoundingBoxVersion.
// It returns the E29 representation of each version in ids, in one query.
func loadBoundingBoxVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListBoundingBoxVersionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		l := boundingBoxVersionLinks(row)
		out[row.BoundingBoxVersionID] = boundingBoxVersionRepresentation(row, l)
	}
	return out, nil
}
