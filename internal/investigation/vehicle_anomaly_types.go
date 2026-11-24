package investigation

import (
	"context"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// vehicleAnomalyType is one kind of vehicle anomaly Watchdog records (E30).
type vehicleAnomalyType struct {
	AnomalyCode string `json:"anomaly_code"`
	Description string `json:"description"`
	Links       links  `json:"links"`
}

// vehicleAnomalyTypesBody is the E30 response. It is not paginated.
type vehicleAnomalyTypesBody struct {
	Items []vehicleAnomalyType `json:"items"`
	Links links                `json:"links"`
}

// vehicleAnomalyTypeRepresentation maps one anomaly type row to E30. Its links
// point at the type itself and at its rule history.
func vehicleAnomalyTypeRepresentation(anomalyCode, description string) vehicleAnomalyType {
	base := "/v1/vehicle-anomaly-types/" + anomalyCode
	return vehicleAnomalyType{
		AnomalyCode: anomalyCode,
		Description: description,
		Links: links{
			"self":          base,
			"rule_versions": base + "/rule-versions",
		},
	}
}

// listVehicleAnomalyTypes serves GET /v1/vehicle-anomaly-types. It takes no
// query parameters; include is not supported here.
func (s *InvestigationService) listVehicleAnomalyTypes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleAnomalyTypesBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListVehicleAnomalyTypes(ctx)
		if err != nil {
			return err
		}
		body.Items = make([]vehicleAnomalyType, 0, len(rows))
		for _, row := range rows {
			body.Items = append(body.Items, vehicleAnomalyTypeRepresentation(row.AnomalyCode, row.Description))
		}
		body.Links = links{"self": linkWithQuery(r.URL.Path, r.URL.Query())}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}
