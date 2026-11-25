package investigation

import (
	"context"
	"net/http"
	"strconv"

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

// kindVehicleAnomalyType is the include kind of one vehicle anomaly type (E31).
// Its ID is anomaly_type_id; the URL uses the code.
const kindVehicleAnomalyType resourceKind = "vehicle_anomaly_type"

// getVehicleAnomalyType serves GET /v1/vehicle-anomaly-types/:anomaly_code (E31).
// It takes no query parameters. An unknown code is a 404 from the no-rows path.
func (s *InvestigationService) getVehicleAnomalyType(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleAnomalyType
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehicleAnomalyType(ctx, ps.ByName("anomaly_code"))
		if err != nil {
			return err
		}
		body = vehicleAnomalyTypeRepresentation(row.AnomalyCode, row.Description)
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// loadVehicleAnomalyTypes is the include loader for kindVehicleAnomalyType. It
// returns each type's E31 representation, keyed by anomaly_type_id, in one query.
func loadVehicleAnomalyTypes(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListVehicleAnomalyTypesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[int64(row.AnomalyTypeID)] = vehicleAnomalyTypeRepresentation(row.AnomalyCode, row.Description)
	}
	return out, nil
}

// vehicleAnomalyRuleVersionsPath is the collection prefix of E33.
const vehicleAnomalyRuleVersionsPath = "/v1/vehicle-anomaly-rule-versions/"

// vehicleAnomalyRuleVersionIncludable is the include names E33 accepts.
var vehicleAnomalyRuleVersionIncludable = map[string]resourceKind{
	"anomaly_type": kindVehicleAnomalyType,
}

// vehicleAnomalyRuleVersion is one rule version of a vehicle anomaly type (E33):
// the detection logic and thresholds Watchdog applied from effective_at on.
type vehicleAnomalyRuleVersion struct {
	RuleVersionID             int64    `json:"rule_version_id"`
	AnomalyType               string   `json:"anomaly_type"`
	RuleLogicVersion          int32    `json:"rule_logic_version"`
	VersionName               string   `json:"version_name"`
	WatchdogBuildVersion      *string  `json:"watchdog_build_version"`
	EffectiveAt               apiTime  `json:"effective_at"`
	StaleAgeThresholdSeconds  *int32   `json:"stale_age_threshold_seconds"`
	ReportedSpeedThresholdMps *float64 `json:"reported_speed_threshold_mps"`
	Links                     links    `json:"links"`
}

// vehicleAnomalyRuleVersionLinks builds a rule version's links. Both always
// exist. The anomaly_type link's ID is anomaly_type_id, the key its loader
// uses; its URL uses the code.
func vehicleAnomalyRuleVersionLinks(row dbgen.GetVehicleAnomalyRuleVersionRow) itemLinks {
	return itemLinks{
		"self": {URL: vehicleAnomalyRuleVersionsPath + strconv.FormatInt(row.RuleVersionID, 10)},
		"anomaly_type": {
			Kind: kindVehicleAnomalyType,
			ID:   int64(row.AnomalyTypeID),
			URL:  "/v1/vehicle-anomaly-types/" + row.AnomalyCode,
		},
	}
}

// vehicleAnomalyRuleVersionRepresentation maps one rule version row to E33.
func vehicleAnomalyRuleVersionRepresentation(row dbgen.GetVehicleAnomalyRuleVersionRow, l itemLinks) vehicleAnomalyRuleVersion {
	return vehicleAnomalyRuleVersion{
		RuleVersionID:             row.RuleVersionID,
		AnomalyType:               row.AnomalyCode,
		RuleLogicVersion:          row.RuleLogicVersion,
		VersionName:               row.VersionName,
		WatchdogBuildVersion:      row.WatchdogBuildVersion,
		EffectiveAt:               newAPITime(row.EffectiveAt),
		StaleAgeThresholdSeconds:  row.StaleAgeThresholdSeconds,
		ReportedSpeedThresholdMps: row.ReportedSpeedThresholdMps,
		Links:                     l.links(),
	}
}

// loadVehicleAnomalyRuleVersions is the include loader for
// kindVehicleAnomalyRuleVersion. It returns each rule version's E33
// representation, keyed by rule_version_id, in one query.
func loadVehicleAnomalyRuleVersions(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.GetVehicleAnomalyRuleVersion(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[row.RuleVersionID] = vehicleAnomalyRuleVersionRepresentation(row, vehicleAnomalyRuleVersionLinks(row))
	}
	return out, nil
}
