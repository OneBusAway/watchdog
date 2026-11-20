-- name: GetUnmatchedTripEpisode :one
SELECT *
FROM unmatched_trip_episode_view
WHERE unmatched_trip_episode_id = $1;

-- name: ListUnmatchedTripEpisodeStaticContexts :many
-- The static trip contexts linked to each episode, for E19 and its include
-- loader, oldest published first. Episode timing comes from the view, because
-- opened_at and closed_at are derived from the OBA responses.
SELECT m.unmatched_trip_episode_id, v.trip_metadata_version_id, v.static_trip_found,
       v.route_id, v.service_id, v.block_id, v.gtfs_static_version_id,
       g.published_at, e.opened_at, e.closed_at
FROM unmatched_trip_episode_metadata m
JOIN trip_metadata_version v ON v.trip_metadata_version_id = m.trip_metadata_version_id
JOIN gtfs_static_version g ON g.gtfs_static_version_id = v.gtfs_static_version_id
JOIN unmatched_trip_episode_view e ON e.unmatched_trip_episode_id = m.unmatched_trip_episode_id
WHERE m.unmatched_trip_episode_id = ANY(sqlc.arg('episode_ids')::bigint[])
ORDER BY m.unmatched_trip_episode_id, g.published_at, g.gtfs_static_version_id;
