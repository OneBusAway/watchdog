package investigation

import (
	"context"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindAnomalyType is the include kind of a vehicle anomaly type (E31). Every
// link named anomaly_type points to one.
const kindAnomalyType resourceKind = "anomaly_type"

// vehicleAnomalyType is one anomaly type in a response (E30, E31).
type vehicleAnomalyType struct {
	AnomalyCode string `json:"anomaly_code"`
	Description string `json:"description"`
	Links       links  `json:"links"`
}

// vehicleAnomalyTypeList is the E30 body. It is not paginated: the set of
// anomaly types is small and changes only with a Watchdog release.
type vehicleAnomalyTypeList struct {
	Items []vehicleAnomalyType `json:"items"`
	Links links                `json:"links"`
}

// listVehicleAnomalyTypes serves GET /v1/vehicle-anomaly-types: every anomaly
// type Watchdog can record, ordered by anomaly_code for stable output.
func (s *InvestigationService) listVehicleAnomalyTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleAnomalyTypeList
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListVehicleAnomalyTypes(ctx)
		if err != nil {
			return err
		}
		body = vehicleAnomalyTypeList{
			Items: anomalyTypeItems(rows),
			Links: links{"self": linkWithQuery(r.URL.Path, r.URL.Query())},
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// anomalyTypeItems maps rows to their representations. The slice is never nil,
// so an empty list encodes as [] rather than null.
func anomalyTypeItems(rows []dbgen.VehicleAnomalyType) []vehicleAnomalyType {
	items := make([]vehicleAnomalyType, 0, len(rows))
	for _, row := range rows {
		items = append(items, anomalyTypeRepresentation(row))
	}
	return items
}

// anomalyTypeRepresentation maps one row to the representation that E30 lists
// and E31 returns.
func anomalyTypeRepresentation(row dbgen.VehicleAnomalyType) vehicleAnomalyType {
	self := "/v1/vehicle-anomaly-types/" + row.AnomalyCode
	return vehicleAnomalyType{
		AnomalyCode: row.AnomalyCode,
		Description: row.Description,
		Links: links{
			"self":          self,
			"rule_versions": self + "/rule-versions",
		},
	}
}

// getVehicleAnomalyType serves GET /v1/vehicle-anomaly-types/:anomaly_code:
// one anomaly type by its code. An unknown code returns 404.
func (s *InvestigationService) getVehicleAnomalyType(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	code := ps.ByName("anomaly_code")
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleAnomalyType
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehicleAnomalyType(ctx, code)
		if err != nil {
			return err
		}
		body = anomalyTypeRepresentation(row)
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// loadAnomalyTypes is the include loader for kindAnomalyType. It returns the
// E31 representation of each anomaly type in ids, in one query. ids are the
// internal anomaly_type_id values.
func loadAnomalyTypes(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListVehicleAnomalyTypesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[int64(row.AnomalyTypeID)] = anomalyTypeRepresentation(row)
	}
	return out, nil
}
