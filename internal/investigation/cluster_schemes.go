package investigation

import (
	"context"
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// clusterScheme is the representation of a spatial cluster scheme (E40).
type clusterScheme struct {
	SchemeVersion   string  `json:"scheme_version"`
	S2Level         int16   `json:"s2_level"`
	StationGrouping string  `json:"station_grouping"`
	Description     string  `json:"description"`
	CreatedAt       apiTime `json:"created_at"`
	Links           links   `json:"links"`
}

// clusterSchemeRepresentation maps a database row to its API representation.
func clusterSchemeRepresentation(row dbgen.ClusterScheme) clusterScheme {
	return clusterScheme{
		SchemeVersion:   row.SchemeVersion,
		S2Level:         row.S2Level,
		StationGrouping: row.StationGrouping,
		Description:     row.Description,
		CreatedAt:       newAPITime(row.CreatedAt),
		Links: links{
			"self": "/v1/cluster-schemes/" + row.SchemeVersion,
		},
	}
}

// getClusterScheme serves GET /v1/cluster-schemes/:scheme_version: one spatial
// cluster scheme. It takes no query parameters; there is nothing to include.
func (s *InvestigationService) getClusterScheme(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	schemeVersion := ps.ByName("scheme_version")

	var row dbgen.ClusterScheme
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		row, err = q.GetClusterScheme(ctx, schemeVersion)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.writeJSON(w, r, clusterSchemeRepresentation(row), cacheImmutable(time.Time{}))
}
