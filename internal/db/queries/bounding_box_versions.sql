-- name: GetBoundingBoxVersion :one
-- One bounding-box version by its ID.
SELECT bounding_box_version_id, gtfs_static_version_id, server_id, agency_id,
       minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude
FROM bounding_box_version
WHERE bounding_box_version_id = $1;

-- name: ListBoundingBoxVersionsByIDs :many
-- The bounding-box versions with the given IDs, for the include loader.
-- IDs with no row are left out.
SELECT bounding_box_version_id, gtfs_static_version_id, server_id, agency_id,
       minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude
FROM bounding_box_version
WHERE bounding_box_version_id = ANY(sqlc.arg('bounding_box_version_ids')::bigint[])
ORDER BY bounding_box_version_id;
