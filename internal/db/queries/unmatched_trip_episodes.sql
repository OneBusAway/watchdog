-- name: GetUnmatchedTripEpisode :one
SELECT *
FROM unmatched_trip_episode_view
WHERE unmatched_trip_episode_id = $1;
