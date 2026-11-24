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
