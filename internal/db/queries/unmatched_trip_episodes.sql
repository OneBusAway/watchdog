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

-- name: ListUnmatchedTripEpisodes :many
-- One page of unmatched-trip episodes, newest first, for E17 and E13.
SELECT *
FROM unmatched_trip_episode_view
WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
  AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
  AND (sqlc.narg('reported_trip_id')::bigint IS NULL OR reported_trip_id = sqlc.narg('reported_trip_id')::bigint)
  AND (sqlc.narg('oba_trip_id')::text IS NULL OR trip_id = sqlc.narg('oba_trip_id')::text)
  AND (sqlc.narg('status')::text IS NULL
       OR (sqlc.narg('status')::text = 'open' AND closing_response_agency_id IS NULL)
       OR (sqlc.narg('status')::text = 'closed' AND closing_response_agency_id IS NOT NULL))
  AND (sqlc.narg('active_to')::timestamptz IS NULL OR opened_at < sqlc.narg('active_to')::timestamptz)
  AND (sqlc.narg('active_from')::timestamptz IS NULL OR closed_at IS NULL
       OR closed_at >= sqlc.narg('active_from')::timestamptz)
  AND (sqlc.narg('close_reason')::text IS NULL OR close_reason = sqlc.narg('close_reason')::text)
  AND (sqlc.narg('static_found')::boolean IS NULL OR static_found = sqlc.narg('static_found')::boolean)
  AND (sqlc.narg('after_id')::bigint IS NULL OR unmatched_trip_episode_id < sqlc.narg('after_id')::bigint)
ORDER BY unmatched_trip_episode_id DESC
LIMIT sqlc.arg('page_limit');
