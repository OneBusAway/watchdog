-- name: GetVehicleAnomalyRuleVersion :one
-- One vehicle anomaly rule version by ID, with its anomaly code.
SELECT
    r.rule_version_id,
    r.anomaly_type_id,
    t.anomaly_code,
    r.rule_logic_version,
    r.version_name,
    r.watchdog_build_version,
    r.effective_at,
    r.stale_age_threshold_seconds,
    r.reported_speed_threshold_mps
FROM vehicle_anomaly_rule_version r
JOIN vehicle_anomaly_type t ON t.anomaly_type_id = r.anomaly_type_id
WHERE r.rule_version_id = $1;
