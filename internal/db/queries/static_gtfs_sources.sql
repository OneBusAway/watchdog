-- name: GetStaticGTFSSource :one
-- One static GTFS source by ID (E36).
SELECT source_id, server_id, normalized_source_url
FROM static_gtfs_source
WHERE source_id = $1;
