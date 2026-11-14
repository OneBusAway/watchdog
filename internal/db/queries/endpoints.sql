-- name: ListServerVehiclePositionEndpoints :many
-- Every vehicle-position feed URL recorded for one OBA server, oldest ID
-- first, including URLs no longer in the configuration.
SELECT endpoint_id, server_id, normalized_vehicle_position_url, created_at
FROM vehicle_position_endpoint
WHERE server_id = $1
ORDER BY endpoint_id;

-- name: GetVehiclePositionEndpoint :one
-- One vehicle-position feed endpoint by its ID.
SELECT endpoint_id, server_id, normalized_vehicle_position_url, created_at
FROM vehicle_position_endpoint
WHERE endpoint_id = $1;

-- name: ListVehiclePositionEndpointsByIDs :many
-- The vehicle-position endpoints with the given IDs, for the include loader.
-- IDs with no row are left out.
SELECT endpoint_id, server_id, normalized_vehicle_position_url, created_at
FROM vehicle_position_endpoint
WHERE endpoint_id = ANY(sqlc.arg('endpoint_ids')::bigint[])
ORDER BY endpoint_id;
