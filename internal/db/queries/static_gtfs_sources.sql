-- name: GetStaticGTFSSource :one
-- One static GTFS source by ID (E36).
SELECT source_id, server_id, normalized_source_url
FROM static_gtfs_source
WHERE source_id = $1;

-- name: ListStaticGTFSSourceVersions :many
SELECT vs.gtfs_static_version_id, v.published_at, vs.source_content_sha256
FROM gtfs_static_version_source AS vs
JOIN gtfs_static_version AS v ON v.gtfs_static_version_id = vs.gtfs_static_version_id
WHERE vs.source_id = sqlc.arg('source_id')
  AND (sqlc.narg('after_id')::bigint IS NULL OR vs.gtfs_static_version_id < sqlc.narg('after_id')::bigint)
ORDER BY vs.gtfs_static_version_id DESC
LIMIT sqlc.arg('page_limit')::integer;
