-- name: GetUnmatchedStopEpisode :one
SELECT *
FROM unmatched_stop_episode_view
WHERE unmatched_stop_episode_id = $1;

-- name: ListUnmatchedStopEpisodes :many
SELECT e.*
FROM unmatched_stop_episode_view e
LEFT JOIN LATERAL (
    SELECT sc_v.stop_metadata_version_id
    FROM unmatched_stop_episode_metadata sc_m
    JOIN stop_metadata_version sc_v ON sc_v.stop_metadata_version_id = sc_m.stop_metadata_version_id
    JOIN gtfs_static_version sc_g ON sc_g.gtfs_static_version_id = sc_v.gtfs_static_version_id
    WHERE sc_m.unmatched_stop_episode_id = e.unmatched_stop_episode_id
      AND (sqlc.narg('open_at')::timestamptz IS NULL OR sc_g.published_at <= sqlc.narg('open_at')::timestamptz)
    ORDER BY sc_g.published_at DESC, sc_g.gtfs_static_version_id DESC
    LIMIT 1
) applied_metadata ON TRUE
LEFT JOIN stop_cluster_assignment sca
    ON sca.stop_metadata_version_id = applied_metadata.stop_metadata_version_id
   AND sca.scheme_version = sqlc.narg('scheme_version')::text
WHERE (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
  AND (sqlc.narg('agency_id')::bigint IS NULL OR e.agency_id = sqlc.narg('agency_id')::bigint)
  AND (sqlc.narg('reported_stop_id')::bigint IS NULL OR e.reported_stop_id = sqlc.narg('reported_stop_id')::bigint)
  AND (sqlc.narg('oba_stop_id')::text IS NULL OR e.stop_id = sqlc.narg('oba_stop_id')::text)
  AND (sqlc.narg('status')::text IS NULL
       OR (sqlc.narg('status')::text = 'open' AND e.closing_response_agency_id IS NULL)
       OR (sqlc.narg('status')::text = 'closed' AND e.closing_response_agency_id IS NOT NULL))
  AND (sqlc.narg('active_to')::timestamptz IS NULL OR e.opened_at < sqlc.narg('active_to')::timestamptz)
  AND (sqlc.narg('active_from')::timestamptz IS NULL OR e.closed_at IS NULL
       OR e.closed_at >= sqlc.narg('active_from')::timestamptz)
  AND (sqlc.narg('opened_from')::timestamptz IS NULL OR e.opened_at >= sqlc.narg('opened_from')::timestamptz)
  AND (sqlc.narg('opened_to')::timestamptz IS NULL OR e.opened_at < sqlc.narg('opened_to')::timestamptz)
  AND (sqlc.narg('open_at')::timestamptz IS NULL
       OR (e.opened_at <= sqlc.narg('open_at')::timestamptz
           AND (e.closed_at IS NULL OR e.closed_at > sqlc.narg('open_at')::timestamptz)))
  AND (sqlc.narg('close_reason')::text IS NULL OR e.close_reason = sqlc.narg('close_reason')::text)
  AND (sqlc.narg('static_found')::boolean IS NULL OR e.static_found = sqlc.narg('static_found')::boolean)
  AND (sqlc.narg('opening_response_id')::bigint IS NULL OR e.opening_response_id = sqlc.narg('opening_response_id')::bigint)
  AND (sqlc.narg('closing_response_id')::bigint IS NULL OR e.closing_response_id = sqlc.narg('closing_response_id')::bigint)
  AND (sqlc.narg('gtfs_static_version_id')::bigint IS NULL OR e.gtfs_static_version_id = sqlc.narg('gtfs_static_version_id')::bigint)
  AND (
      CASE
          WHEN sqlc.narg('recurrence')::text = 'has_previous_episode' THEN e.previous_episode_count > 0
          WHEN sqlc.narg('recurrence')::text = 'no_previous_episode' THEN e.previous_episode_count = 0
          ELSE TRUE
      END
  )
  AND (sqlc.narg('cell_id')::text IS NULL OR sca.cell_id = sqlc.narg('cell_id')::text)
  AND (sqlc.narg('root_station_id')::text IS NULL OR sca.root_station_id = sqlc.narg('root_station_id')::text)
  AND (sqlc.narg('after_id')::bigint IS NULL OR e.unmatched_stop_episode_id < sqlc.narg('after_id')::bigint)
ORDER BY e.unmatched_stop_episode_id DESC
LIMIT sqlc.arg('page_limit');

-- name: ListUnmatchedStopEpisodeStaticContexts :many
-- The static contexts linked to each episode, for E16 and its include loader,
-- oldest published first.
SELECT m.unmatched_stop_episode_id, v.stop_metadata_version_id, v.gtfs_static_version_id,
       v.static_stop_found, v.stop_name, v.latitude, v.longitude, v.parent_station_id,
       v.location_type, g.published_at, e.opened_at, e.closed_at
FROM unmatched_stop_episode_metadata m
JOIN stop_metadata_version v ON v.stop_metadata_version_id = m.stop_metadata_version_id
JOIN gtfs_static_version g ON g.gtfs_static_version_id = v.gtfs_static_version_id
JOIN unmatched_stop_episode_view e ON e.unmatched_stop_episode_id = m.unmatched_stop_episode_id
WHERE m.unmatched_stop_episode_id = ANY(sqlc.arg('episode_ids')::bigint[])
ORDER BY m.unmatched_stop_episode_id, g.published_at, g.gtfs_static_version_id;
