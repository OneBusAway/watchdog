-- name: GetVehiclePositionResponse :many
-- The vehicle position responses with the given IDs (E28). One query serves the
-- endpoint (one ID) and the include loader (many IDs).
SELECT vehicle_position_response_id,
       endpoint_id,
       observed_at,
       source_header_time,
       incrementality,
       payload_sha256
FROM vehicle_position_response
WHERE vehicle_position_response_id = ANY(sqlc.arg('vehicle_position_response_ids')::bigint[])
ORDER BY vehicle_position_response_id;
