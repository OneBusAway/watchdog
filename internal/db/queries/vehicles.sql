-- name: ListVehicles :many
SELECT v.vehicle_entity_id, v.endpoint_id, v.external_vehicle_id
FROM vehicle_entity v
  JOIN vehicle_position_endpoint e USING (endpoint_id)
WHERE v.external_vehicle_id = sqlc.arg('external_vehicle_id')::text
  AND (sqlc.narg('endpoint_id')::bigint IS NULL OR v.endpoint_id = sqlc.narg('endpoint_id')::bigint)
  AND (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
ORDER BY v.vehicle_entity_id;
