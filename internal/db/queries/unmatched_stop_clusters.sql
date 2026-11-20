-- name: GetLatestClusterScheme :one
-- The newest cluster scheme by created_at, the default scheme_version for E38.
SELECT scheme_version, s2_level, station_grouping, description, created_at
FROM cluster_scheme
ORDER BY created_at DESC, scheme_version DESC
LIMIT 1;

-- name: ListUnmatchedStopClusterMembers :many
-- One row per unmatched-stop episode open at open_at, for E38. The context that applies
-- at open_at is the episode's linked stop_metadata_version whose static version has the
-- greatest published_at <= open_at. Its assignment for scheme_version gives the cell and
-- root station; a missing context or assignment leaves those columns NULL. Grouping and
-- ordering are done in Go. The schema holds everything needed, so no migration is required.
SELECT e.unmatched_stop_episode_id,
       sa.cell_id,
       sa.root_station_id,
       sa.root_station_name,
       c.cell_level,
       c.center_latitude,
       c.center_longitude
FROM unmatched_stop_episode_view e
LEFT JOIN LATERAL (
    SELECT m.stop_metadata_version_id
    FROM unmatched_stop_episode_metadata m
    JOIN stop_metadata_version v ON v.stop_metadata_version_id = m.stop_metadata_version_id
    JOIN gtfs_static_version g ON g.gtfs_static_version_id = v.gtfs_static_version_id
    WHERE m.unmatched_stop_episode_id = e.unmatched_stop_episode_id
      AND g.published_at <= sqlc.arg('open_at')::timestamptz
    ORDER BY g.published_at DESC, g.gtfs_static_version_id DESC
    LIMIT 1
) ctx ON TRUE
LEFT JOIN stop_cluster_assignment sa
    ON sa.stop_metadata_version_id = ctx.stop_metadata_version_id
   AND sa.scheme_version = sqlc.narg('scheme_version')::text
LEFT JOIN s2_cell c
    ON c.scheme_version = sa.scheme_version AND c.cell_id = sa.cell_id
WHERE e.opened_at <= sqlc.arg('open_at')::timestamptz
  AND (e.closed_at IS NULL OR e.closed_at > sqlc.arg('open_at')::timestamptz)
  AND e.server_id = sqlc.arg('server_id')::bigint
  AND (sqlc.narg('agency_id')::bigint IS NULL OR e.agency_id = sqlc.narg('agency_id')::bigint)
ORDER BY e.unmatched_stop_episode_id;
