-- name: GetStaticGtfsSource :one
-- One static GTFS source by ID.
SELECT source_id, server_id, normalized_source_url
FROM static_gtfs_source
WHERE source_id = $1;

-- name: ListStaticGtfsSourcesByIDs :many
-- The static GTFS sources with the given IDs, for the include loader. IDs
-- with no row are left out.
SELECT source_id, server_id, normalized_source_url
FROM static_gtfs_source
WHERE source_id = ANY(sqlc.arg('source_ids')::bigint[])
ORDER BY source_id;
