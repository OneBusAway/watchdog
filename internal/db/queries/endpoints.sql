-- name: ListServerVehiclePositionEndpoints :many
-- Every vehicle-position feed URL recorded for one OBA server, oldest ID
-- first, including URLs no longer in the configuration.
SELECT endpoint_id, server_id, normalized_vehicle_position_url, created_at
FROM vehicle_position_endpoint
WHERE server_id = $1
ORDER BY endpoint_id;
