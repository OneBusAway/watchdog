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

// boundingBoxVersionIncludable maps each link that can be included (E29)
// to its kind.
var boundingBoxVersionIncludable = map[string]resourceKind{
	"gtfs_static_version": kindGTFSStaticVersion,
	"server":              kindServer,
	"agency":              kindAgency,
}

// boundingBoxVersion is the E29 representation of one bounding box version.
type boundingBoxVersion struct {
	BoundingBoxVersionID int64   `json:"bounding_box_version_id"`
	GtfsStaticVersionID  int64   `json:"gtfs_static_version_id"`
	ServerID             int64   `json:"server_id"`
	AgencyID             *int64  `json:"agency_id"`
	Scope                string  `json:"scope"`
	MinimumLatitude      float64 `json:"minimum_latitude"`
	MaximumLatitude      float64 `json:"maximum_latitude"`
	MinimumLongitude     float64 `json:"minimum_longitude"`
	MaximumLongitude     float64 `json:"maximum_longitude"`
	Links                links   `json:"links"`
}

// boundingBoxVersionBody is the E29 response.
type boundingBoxVersionBody struct {
	boundingBoxVersion
	Included included `json:"included,omitzero"`
}

// boundingBoxVersionLinks builds the links for one bounding box version (E29).
// self, gtfs_static_version, and server are always present; agency is present
// only when agency_id is not null.
func boundingBoxVersionLinks(row dbgen.BoundingBoxVersion) itemLinks {
	self := "/v1/bounding-box-versions/" + strconv.FormatInt(row.BoundingBoxVersionID, 10)
	l := itemLinks{
		"self": {URL: self},
		"gtfs_static_version": {
			Kind: kindGTFSStaticVersion,
			ID:   row.GtfsStaticVersionID,
			URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(row.GtfsStaticVersionID, 10),
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
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

// boundingBoxVersionRepresentation maps one row to its E29 representation.
func boundingBoxVersionRepresentation(row dbgen.BoundingBoxVersion, l itemLinks) boundingBoxVersion {
	scope := "server"
	if row.AgencyID != nil {
		scope = "agency"
	}
	return boundingBoxVersion{
		BoundingBoxVersionID: row.BoundingBoxVersionID,
		GtfsStaticVersionID:  row.GtfsStaticVersionID,
		ServerID:             row.ServerID,
		AgencyID:             row.AgencyID,
		Scope:                scope,
		MinimumLatitude:      row.MinimumLatitude,
		MaximumLatitude:      row.MaximumLatitude,
		MinimumLongitude:     row.MinimumLongitude,
		MaximumLongitude:     row.MaximumLongitude,
		Links:                l.links(),
	}
}

// getBoundingBoxVersion serves GET /v1/bounding-box-versions/:bounding_box_version_id (E29).
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
		rows, err := q.GetBoundingBoxVersion(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return pgx.ErrNoRows
		}
		l := boundingBoxVersionLinks(rows[0])
		body.boundingBoxVersion = boundingBoxVersionRepresentation(rows[0], l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Bounding boxes never change once written, so the representation is immutable.
	// No Last-Modified: the record has no single time to offer. Any include gets
	// the short policy, as in E16, E18, E19 and E27.
	cache := cacheImmutable(time.Time{})
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}

// loadBoundingBoxVersions is the include loader for kindBoundingBoxVersion.
// It returns each record's E29 representation, keyed by its ID, in one query.
func loadBoundingBoxVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.GetBoundingBoxVersion(ctx, ids)
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
