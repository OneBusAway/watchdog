-- name: ListVehicleAnomalyTypes :many
-- Every vehicle anomaly type, ordered by anomaly_code for stable output.
SELECT anomaly_type_id, anomaly_code, description
FROM vehicle_anomaly_type
ORDER BY anomaly_code;

-- name: GetVehicleAnomalyType :one
-- One vehicle anomaly type by its code.
SELECT anomaly_type_id, anomaly_code, description
FROM vehicle_anomaly_type
WHERE anomaly_code = $1;

-- name: ListVehicleAnomalyTypesByIDs :many
-- The vehicle anomaly types with the given IDs, for the include loader.
-- IDs with no row are left out.
SELECT anomaly_type_id, anomaly_code, description
FROM vehicle_anomaly_type
WHERE anomaly_type_id = ANY(sqlc.arg('anomaly_type_ids')::bigint[])
ORDER BY anomaly_code;
