-- name: ListVehicleAnomalyTypes :many
-- Every vehicle anomaly type, ordered by code, for E30.
SELECT anomaly_code, description
FROM vehicle_anomaly_type
ORDER BY anomaly_code;

-- name: GetVehicleAnomalyType :one
-- One vehicle anomaly type by its code, for E31.
SELECT anomaly_code, description
FROM vehicle_anomaly_type
WHERE anomaly_code = sqlc.arg('anomaly_code');

-- name: ListVehicleAnomalyTypesByIDs :many
-- The vehicle anomaly types with the given IDs, for the include loader (E31).
SELECT anomaly_type_id, anomaly_code, description
FROM vehicle_anomaly_type
WHERE anomaly_type_id = ANY(sqlc.arg('anomaly_type_ids')::bigint[])
ORDER BY anomaly_type_id;

-- name: GetVehicleAnomalyRuleVersion :many
-- The rule versions with the given IDs and their anomaly type, for E33 and its
-- include loader. Array form so the loader batches in one query.
SELECT r.rule_version_id, r.anomaly_type_id, t.anomaly_code,
       r.rule_logic_version, r.version_name, r.watchdog_build_version,
       r.effective_at, r.stale_age_threshold_seconds, r.reported_speed_threshold_mps
FROM vehicle_anomaly_rule_version r
JOIN vehicle_anomaly_type t ON t.anomaly_type_id = r.anomaly_type_id
WHERE r.rule_version_id = ANY(sqlc.arg('rule_version_ids')::bigint[])
ORDER BY r.rule_version_id;

-- name: ListVehicleAnomalyRuleVersions :many
-- The rule versions of one anomaly type, newest effective_at first, for E32.
-- effective_at is unique per type, so the order has no ties.
SELECT r.rule_version_id, r.anomaly_type_id, t.anomaly_code,
       r.rule_logic_version, r.version_name, r.watchdog_build_version,
       r.effective_at, r.stale_age_threshold_seconds, r.reported_speed_threshold_mps
FROM vehicle_anomaly_rule_version r
JOIN vehicle_anomaly_type t ON t.anomaly_type_id = r.anomaly_type_id
WHERE t.anomaly_code = sqlc.arg('anomaly_code')
ORDER BY r.effective_at DESC;
