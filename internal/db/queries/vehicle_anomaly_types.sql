-- name: ListVehicleAnomalyTypes :many
-- Every vehicle anomaly type, ordered by code, for E30.
SELECT anomaly_code, description
FROM vehicle_anomaly_type
ORDER BY anomaly_code;
