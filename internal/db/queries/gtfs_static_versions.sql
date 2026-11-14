-- name: GetGTFSStaticVersion :one
SELECT
    gtfs_static_version_id,
    server_id,
    published_at,
    combined_content_sha256,
    parser_version
FROM gtfs_static_version
WHERE gtfs_static_version_id = $1;

-- name: ListGTFSStaticVersionsByIDs :many
SELECT
    gtfs_static_version_id,
    server_id,
    published_at,
    combined_content_sha256,
    parser_version
FROM gtfs_static_version
WHERE gtfs_static_version_id = ANY($1::bigint[])
ORDER BY gtfs_static_version_id;

-- name: ListGTFSStaticVersionSources :many
-- Sources (with their URL) for one version, ordered by source_id ascending.
SELECT
    vs.source_id,
    s.normalized_source_url,
    vs.source_content_sha256
FROM gtfs_static_version_source vs
JOIN static_gtfs_source s ON s.source_id = vs.source_id
WHERE vs.gtfs_static_version_id = $1
ORDER BY vs.source_id;

-- name: ListGTFSStaticVersionSourcesByVersionIDs :many
-- Sources for a batch of versions, for the include loader.
SELECT
    vs.gtfs_static_version_id,
    vs.source_id,
    s.normalized_source_url,
    vs.source_content_sha256
FROM gtfs_static_version_source vs
JOIN static_gtfs_source s ON s.source_id = vs.source_id
WHERE vs.gtfs_static_version_id = ANY($1::bigint[])
ORDER BY vs.gtfs_static_version_id, vs.source_id;

-- name: ListGTFSStaticVersionAgencies :many
-- Agencies for one version, ordered by agency_id ascending.
SELECT
    va.agency_id,
    a.external_agency_id
FROM gtfs_static_version_agency va
JOIN agency a ON a.agency_id = va.agency_id
WHERE va.gtfs_static_version_id = $1
ORDER BY va.agency_id;

-- name: ListGTFSStaticVersionAgenciesByVersionIDs :many
-- Agencies for a batch of versions, for the include loader.
SELECT
    va.gtfs_static_version_id,
    va.agency_id,
    a.external_agency_id
FROM gtfs_static_version_agency va
JOIN agency a ON a.agency_id = va.agency_id
WHERE va.gtfs_static_version_id = ANY($1::bigint[])
ORDER BY va.gtfs_static_version_id, va.agency_id;
