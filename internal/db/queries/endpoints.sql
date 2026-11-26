-- name: ListServerVehiclePositionEndpoints :many
-- Every vehicle-position feed URL recorded for one OBA server, oldest ID
-- first, including URLs no longer in the configuration.
SELECT endpoint_id, server_id, normalized_vehicle_position_url, created_at
FROM vehicle_position_endpoint
WHERE server_id = $1
ORDER BY endpoint_id;

-- name: GetVehiclePositionEndpoint :one
-- One vehicle-position feed URL by ID (E07), with its server's base URL,
-- which selects the configuration entries that decide configured.
SELECT sqlc.embed(e), s.normalized_base_url
FROM vehicle_position_endpoint e
JOIN onebusaway_server s ON s.server_id = e.server_id
WHERE e.endpoint_id = $1;

-- name: ListVehiclePositionEndpointsByIDs :many
-- The vehicle-position feed URLs with the given IDs and their servers' base
-- URLs, for the include loader. IDs with no row are left out.
SELECT sqlc.embed(e), s.normalized_base_url
FROM vehicle_position_endpoint e
JOIN onebusaway_server s ON s.server_id = e.server_id
WHERE e.endpoint_id = ANY(sqlc.arg('endpoint_ids')::bigint[])
ORDER BY e.endpoint_id;
