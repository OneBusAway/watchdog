-- name: ListGTFSStaticVersions :many
SELECT gtfs_static_version_id, server_id, published_at, combined_content_sha256, parser_version
FROM gtfs_static_version
WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
  AND (sqlc.narg('published_from')::timestamptz IS NULL OR published_at >= sqlc.narg('published_from')::timestamptz)
  AND (sqlc.narg('published_to')::timestamptz IS NULL OR published_at < sqlc.narg('published_to')::timestamptz)
  AND (sqlc.narg('after_id')::bigint IS NULL OR gtfs_static_version_id < sqlc.narg('after_id')::bigint)
ORDER BY gtfs_static_version_id DESC
LIMIT sqlc.narg('page_limit')::integer;

-- name: GetGTFSStaticVersion :many
-- The static versions with the given IDs; IDs with no row are left out. This
-- and the two queries below are E35's reads. Each takes a batch of IDs, so E35
-- (one ID) and its include loader (every ID a response links to) run the same
-- three queries, however many IDs there are.
SELECT gtfs_static_version_id, server_id, published_at, combined_content_sha256, parser_version
FROM gtfs_static_version
WHERE gtfs_static_version_id = ANY(sqlc.arg('gtfs_static_version_ids')::bigint[])
ORDER BY gtfs_static_version_id;

-- name: ListGTFSStaticVersionSources :many
-- The source files the given versions were built from, with each source's URL
-- and its content hash in that version, by version and then source_id.
SELECT vs.gtfs_static_version_id, vs.source_id, s.normalized_source_url, vs.source_content_sha256
FROM gtfs_static_version_source AS vs
JOIN static_gtfs_source AS s ON s.source_id = vs.source_id
WHERE vs.gtfs_static_version_id = ANY(sqlc.arg('gtfs_static_version_ids')::bigint[])
ORDER BY vs.gtfs_static_version_id, vs.source_id;

-- name: ListGTFSStaticVersionAgencies :many
-- The agencies the given versions covered, with each agency's OBA agency ID,
-- by version and then agency_id.
SELECT va.gtfs_static_version_id, va.agency_id, a.external_agency_id
FROM gtfs_static_version_agency AS va
JOIN agency AS a ON a.agency_id = va.agency_id
WHERE va.gtfs_static_version_id = ANY(sqlc.arg('gtfs_static_version_ids')::bigint[])
ORDER BY va.gtfs_static_version_id, va.agency_id;
