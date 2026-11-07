-- name: ListServers :many
-- Every OBA server Watchdog has recorded, oldest ID first. With
-- normalized_base_url set, only the server stored under that URL (zero or one
-- row: the column is unique).
SELECT server_id, normalized_base_url, display_name, created_at
FROM onebusaway_server
WHERE sqlc.narg('normalized_base_url')::text IS NULL
   OR normalized_base_url = sqlc.narg('normalized_base_url')
ORDER BY server_id;

-- name: GetServer :one
-- One OBA server by ID.
SELECT server_id, normalized_base_url, display_name, created_at
FROM onebusaway_server
WHERE server_id = $1;

-- name: ListServersByIDs :many
-- The OBA servers with the given IDs, for the include loader. IDs with no row
-- are left out.
SELECT server_id, normalized_base_url, display_name, created_at
FROM onebusaway_server
WHERE server_id = ANY(sqlc.arg('server_ids')::bigint[])
ORDER BY server_id;
