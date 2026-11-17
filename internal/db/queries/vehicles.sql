-- name: ListVehicles :many
SELECT v.vehicle_entity_id, v.endpoint_id, v.external_vehicle_id
FROM vehicle_entity v
  JOIN vehicle_position_endpoint e USING (endpoint_id)
WHERE v.external_vehicle_id = sqlc.arg('external_vehicle_id')::text
  AND (sqlc.narg('endpoint_id')::bigint IS NULL OR v.endpoint_id = sqlc.narg('endpoint_id')::bigint)
  AND (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
ORDER BY v.vehicle_entity_id;

-- name: GetVehicle :one
SELECT vehicle_entity_id, endpoint_id, external_vehicle_id
FROM vehicle_entity
WHERE vehicle_entity_id = sqlc.arg('vehicle_entity_id');

-- name: ListVehiclesByIDs :many
-- Every vehicle in the given IDs, for the include loader's single batched read.
SELECT vehicle_entity_id, endpoint_id, external_vehicle_id
FROM vehicle_entity
WHERE vehicle_entity_id = ANY(sqlc.arg('vehicle_entity_ids')::bigint[])
ORDER BY vehicle_entity_id;
