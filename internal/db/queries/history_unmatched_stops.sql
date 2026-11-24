-- Writes used by internal/history to record OBA metrics responses,
-- unmatched-stop episodes, their stop metadata and cluster assignments.
-- They run only inside a write that opens or closes an episode, saves
-- last_reported_at, or records a new static version (D31).

-- name: InsertOBAMetricsResponse :one
INSERT INTO onebusaway_metrics_response (server_id, observed_at, upstream_current_time, api_version)
VALUES (@server_id, @observed_at, sqlc.narg('upstream_current_time'), sqlc.narg('api_version'))
RETURNING response_id;

-- name: InsertOBAMetricsResponseAgency :one
-- A count is null when the response map has no key for the agency.
INSERT INTO onebusaway_metrics_response_agency (
    response_id, agency_id, response_complete,
    unmatched_stop_count, unmatched_trip_count, matched_stop_count, matched_trip_count
)
VALUES (
    @response_id, @agency_id, @response_complete,
    sqlc.narg('unmatched_stop_count'), sqlc.narg('unmatched_trip_count'),
    sqlc.narg('matched_stop_count'), sqlc.narg('matched_trip_count')
)
RETURNING response_agency_id;

-- name: ListOpenUnmatchedStopEpisodesForServer :many
-- The one read outside a write: the first poll for a server after startup
-- loads that server's open episodes into memory.
SELECT
    e.unmatched_stop_episode_id,
    e.reported_stop_id,
    e.agency_id,
    a.external_agency_id,
    s.oba_stop_id,
    e.last_reported_at
FROM unmatched_stop_episode e
JOIN agency a ON a.agency_id = e.agency_id
JOIN onebusaway_server srv ON srv.server_id = a.server_id
JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id
WHERE srv.normalized_base_url = @normalized_base_url
  AND e.closing_response_agency_id IS NULL;

-- name: InsertUnmatchedStopEpisode :one
INSERT INTO unmatched_stop_episode (
    reported_stop_id, agency_id, first_response_agency_id, last_reported_at
)
VALUES (@reported_stop_id, @agency_id, @first_response_agency_id, @last_reported_at)
RETURNING unmatched_stop_episode_id;

-- name: CloseUnmatchedStopEpisode :exec
-- last_reported_at is the time the stop was last reported, held in memory,
-- so a closed episode is always exact.
UPDATE unmatched_stop_episode
SET closing_response_agency_id = @closing_response_agency_id::bigint,
    close_reason = 'not_reported',
    last_reported_at = @last_reported_at
WHERE unmatched_stop_episode_id = @unmatched_stop_episode_id
  AND closing_response_agency_id IS NULL;

-- name: SaveUnmatchedStopLastReported :exec
-- Saves the last_reported_at of several open episodes in one statement. Times
-- are passed as microseconds since the Unix epoch, which converts exactly. A
-- stored value is only ever moved forward.
UPDATE unmatched_stop_episode e
SET last_reported_at = 'epoch'::timestamptz + u.last_reported_us * interval '1 microsecond'
FROM unnest(@episode_ids::bigint[], @last_reported_us::bigint[]) AS u(episode_id, last_reported_us)
WHERE e.unmatched_stop_episode_id = u.episode_id
  AND e.closing_response_agency_id IS NULL
  AND e.last_reported_at < 'epoch'::timestamptz + u.last_reported_us * interval '1 microsecond';

-- name: GetNewestStopMetadataForStop :one
-- The newest metadata row of a reported stop, by its version's published_at.
SELECT sqlc.embed(v)
FROM stop_metadata_version v
JOIN gtfs_static_version g ON g.gtfs_static_version_id = v.gtfs_static_version_id
WHERE v.reported_stop_id = @reported_stop_id
ORDER BY g.published_at DESC, v.stop_metadata_version_id DESC
LIMIT 1;

-- name: GetNewestLinkedStopMetadata :one
-- The newest metadata row linked to an episode, by its version's published_at.
SELECT sqlc.embed(v)
FROM unmatched_stop_episode_metadata m
JOIN stop_metadata_version v ON v.stop_metadata_version_id = m.stop_metadata_version_id
JOIN gtfs_static_version g ON g.gtfs_static_version_id = v.gtfs_static_version_id
WHERE m.unmatched_stop_episode_id = @unmatched_stop_episode_id
ORDER BY g.published_at DESC, v.stop_metadata_version_id DESC
LIMIT 1;

-- name: InsertStopMetadataVersion :one
-- Returns the row for (version, stop), inserting it when it does not exist.
-- created is false when an existing row was reused.
WITH inserted AS (
    INSERT INTO stop_metadata_version (
        gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found,
        stop_name, latitude, longitude, parent_station_id, location_type
    )
    VALUES (
        @gtfs_static_version_id, @agency_id, @reported_stop_id, @static_stop_found,
        sqlc.narg('stop_name'), sqlc.narg('latitude'), sqlc.narg('longitude'),
        sqlc.narg('parent_station_id'), sqlc.narg('location_type')
    )
    ON CONFLICT (gtfs_static_version_id, reported_stop_id) DO NOTHING
    RETURNING stop_metadata_version_id
)
SELECT stop_metadata_version_id, TRUE::boolean AS created FROM inserted
UNION ALL
SELECT stop_metadata_version_id, FALSE::boolean AS created FROM stop_metadata_version
WHERE gtfs_static_version_id = @gtfs_static_version_id AND reported_stop_id = @reported_stop_id
LIMIT 1;

-- name: LinkUnmatchedStopEpisodeMetadata :exec
INSERT INTO unmatched_stop_episode_metadata (
    unmatched_stop_episode_id, stop_metadata_version_id, reported_stop_id, agency_id
)
VALUES (@unmatched_stop_episode_id, @stop_metadata_version_id, @reported_stop_id, @agency_id)
ON CONFLICT DO NOTHING;

-- name: EnsureClusterScheme :exec
INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description)
VALUES (@scheme_version, @s2_level, @station_grouping, @description)
ON CONFLICT (scheme_version) DO NOTHING;

-- name: UpsertS2Cell :exec
-- A cell's centre is a function of its ID, so an existing row never changes.
INSERT INTO s2_cell (scheme_version, cell_id, cell_level, center_latitude, center_longitude)
VALUES (@scheme_version, @cell_id, @cell_level, @center_latitude, @center_longitude)
ON CONFLICT (scheme_version, cell_id) DO NOTHING;

-- name: InsertStopClusterAssignment :exec
INSERT INTO stop_cluster_assignment (
    stop_metadata_version_id, scheme_version, cell_id, root_station_id, root_station_name
)
VALUES (
    @stop_metadata_version_id, @scheme_version, @cell_id,
    sqlc.narg('root_station_id'), sqlc.narg('root_station_name')
)
ON CONFLICT (stop_metadata_version_id, scheme_version) DO NOTHING;
