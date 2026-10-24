-- name: GetOneBusAwayServerByURL :one
SELECT server_id, normalized_base_url, display_name, created_at
FROM onebusaway_server
WHERE normalized_base_url = $1;
