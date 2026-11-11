package investigation

import (
	"context"
	"net/http"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// vehicleAnomalyRuleVersion is one rule version in a response (E33).
type vehicleAnomalyRuleVersion struct {
	RuleVersionID             int64    `json:"rule_version_id"`
	AnomalyTypeID             int16    `json:"anomaly_type_id"`
	AnomalyCode               string   `json:"anomaly_code"`
	RuleLogicVersion          int32    `json:"rule_logic_version"`
	VersionName               string   `json:"version_name"`
	WatchdogBuildVersion      *string  `json:"watchdog_build_version"`
	EffectiveAt               apiTime  `json:"effective_at"`
	StaleAgeThresholdSeconds  *int32   `json:"stale_age_threshold_seconds"`
	ReportedSpeedThresholdMps *float64 `json:"reported_speed_threshold_mps"`
	Links                     links    `json:"links"`
}

// getVehicleAnomalyRuleVersion serves
// GET /v1/vehicle-anomaly-rule-versions/:rule_version_id (E33).
// Rule versions are immutable once written, so the response uses
// cacheImmutable with effective_at as the Last-Modified time.
func (s *InvestigationService) getVehicleAnomalyRuleVersion(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "rule_version_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var row dbgen.GetVehicleAnomalyRuleVersionRow
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		row, err = q.GetVehicleAnomalyRuleVersion(ctx, id)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, vehicleAnomalyRuleVersionRepresentation(row), cacheImmutable(row.EffectiveAt))
}

// vehicleAnomalyRuleVersionRepresentation maps one row to its E33 representation.
func vehicleAnomalyRuleVersionRepresentation(row dbgen.GetVehicleAnomalyRuleVersionRow) vehicleAnomalyRuleVersion {
	id := strconv.FormatInt(row.RuleVersionID, 10)
	typeID := strconv.FormatInt(int64(row.AnomalyTypeID), 10)
	return vehicleAnomalyRuleVersion{
		RuleVersionID:             row.RuleVersionID,
		AnomalyTypeID:             row.AnomalyTypeID,
		AnomalyCode:               row.AnomalyCode,
		RuleLogicVersion:          row.RuleLogicVersion,
		VersionName:               row.VersionName,
		WatchdogBuildVersion:      row.WatchdogBuildVersion,
		EffectiveAt:               newAPITime(row.EffectiveAt),
		StaleAgeThresholdSeconds:  row.StaleAgeThresholdSeconds,
		ReportedSpeedThresholdMps: row.ReportedSpeedThresholdMps,
		Links: links{
			"self":         "/v1/vehicle-anomaly-rule-versions/" + id,
			"anomaly_type": "/v1/vehicle-anomaly-types/" + typeID,
		},
	}
}
