-- name: GetClusterScheme :one
SELECT scheme_version, s2_level, station_grouping, description, created_at
FROM cluster_scheme
WHERE scheme_version = $1;

-- name: ListClusterSchemes :many
SELECT scheme_version, s2_level, station_grouping, description, created_at
FROM cluster_scheme
ORDER BY created_at DESC;

