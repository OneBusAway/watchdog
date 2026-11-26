-- name: GetBoundingBoxVersion :many
-- One bounding-box version by its ID (E29), or multiple for include loaders.
SELECT bounding_box_version_id, gtfs_static_version_id, server_id, agency_id,
       minimum_latitude, maximum_latitude, minimum_longitude, maximum_longitude
FROM bounding_box_version
WHERE bounding_box_version_id = ANY(sqlc.arg('bounding_box_version_ids')::bigint[])
ORDER BY bounding_box_version_id;
