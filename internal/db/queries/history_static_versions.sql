-- name: GetLatestGTFSStaticVersionForAgencies :one
SELECT v.gtfs_static_version_id, v.combined_content_sha256
FROM gtfs_static_version v
CROSS JOIN LATERAL (
    SELECT coalesce(array_agg(a.external_agency_id), '{}'::text[]) AS agency_ids
    FROM gtfs_static_version_agency va
    JOIN agency a ON a.agency_id = va.agency_id
    WHERE va.gtfs_static_version_id = v.gtfs_static_version_id
) recorded
WHERE v.server_id = @server_id
  AND recorded.agency_ids @> @agency_ids::text[]
  AND recorded.agency_ids <@ @agency_ids::text[]
ORDER BY v.published_at DESC, v.gtfs_static_version_id DESC
LIMIT 1;

-- name: GetLatestGTFSStaticVersionForServer :one
-- The newest server-scoped version, whatever its agencies, and whether its
-- agency set is exactly @agency_ids. Agency-scoped versions are excluded:
-- those entries share this server row but publish their own snapshots, and
-- treating one as newer makes an unchanged server restart insert a duplicate.
SELECT v.gtfs_static_version_id, v.combined_content_sha256,
    (recorded.agency_ids @> @agency_ids::text[]
        AND recorded.agency_ids <@ @agency_ids::text[])::boolean AS same_agencies
FROM gtfs_static_version v
CROSS JOIN LATERAL (
    SELECT coalesce(array_agg(a.external_agency_id), '{}'::text[]) AS agency_ids
    FROM gtfs_static_version_agency va
    JOIN agency a ON a.agency_id = va.agency_id
    WHERE va.gtfs_static_version_id = v.gtfs_static_version_id
) recorded
WHERE v.server_id = @server_id
  AND v.server_scoped
ORDER BY v.published_at DESC, v.gtfs_static_version_id DESC
LIMIT 1;

-- name: InsertGTFSStaticVersion :one
INSERT INTO gtfs_static_version (
    server_id, published_at, combined_content_sha256, parser_version, server_scoped
)
VALUES (
    @server_id, @published_at, @combined_content_sha256, @parser_version, @server_scoped
)
RETURNING gtfs_static_version_id;

-- name: ListRecordedStaticVersionSourceIDs :many
-- Sources already linked to a version. A reused version keeps those rows and
-- only gains a link for a feed URL it did not have.
SELECT source_id
FROM gtfs_static_version_source
WHERE gtfs_static_version_id = @gtfs_static_version_id
ORDER BY source_id;

-- name: InsertGTFSStaticVersionSource :exec
INSERT INTO gtfs_static_version_source (gtfs_static_version_id, source_id, source_content_sha256)
VALUES (@gtfs_static_version_id, @source_id, @source_content_sha256);

-- name: InsertGTFSStaticVersionAgency :exec
INSERT INTO gtfs_static_version_agency (gtfs_static_version_id, agency_id)
VALUES (@gtfs_static_version_id, @agency_id);

-- name: InsertBoundingBoxVersion :one
INSERT INTO bounding_box_version (
    gtfs_static_version_id, server_id, agency_id,
    minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude
)
VALUES (
    @gtfs_static_version_id, @server_id, sqlc.narg('agency_id'),
    @minimum_latitude, @maximum_latitude, @minimum_longitude, @maximum_longitude
)
RETURNING bounding_box_version_id;

-- name: ListBoundingBoxVersionsForStaticVersion :many
SELECT b.bounding_box_version_id, a.external_agency_id
FROM bounding_box_version b
LEFT JOIN agency a ON a.agency_id = b.agency_id
WHERE b.gtfs_static_version_id = @gtfs_static_version_id
ORDER BY b.bounding_box_version_id;
