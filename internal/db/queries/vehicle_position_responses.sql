-- name: GetVehiclePositionResponse :one
-- One vehicle-position response by its ID.
SELECT vehicle_position_response_id, endpoint_id, observed_at,
       source_header_time, incrementality, payload_sha256
FROM vehicle_position_response
WHERE vehicle_position_response_id = $1;

-- name: ListVehiclePositionResponsesByIDs :many
-- The vehicle-position responses with the given IDs, for the include loader.
-- IDs with no row are left out.
SELECT vehicle_position_response_id, endpoint_id, observed_at,
       source_header_time, incrementality, payload_sha256
FROM vehicle_position_response
WHERE vehicle_position_response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY vehicle_position_response_id;
