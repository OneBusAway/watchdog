-- name: GetLatestGTFSStaticVersionForServer :one
SELECT gtfs_static_version_id, combined_content_sha256
FROM gtfs_static_version
WHERE server_id = @server_id
ORDER BY published_at DESC, gtfs_static_version_id DESC
LIMIT 1;

-- name: InsertGTFSStaticVersion :one
INSERT INTO gtfs_static_version (server_id, published_at, combined_content_sha256, parser_version)
VALUES (@server_id, @published_at, @combined_content_sha256, @parser_version)
RETURNING gtfs_static_version_id;

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
