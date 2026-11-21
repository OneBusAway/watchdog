-- name: CountUnmatchedStopEpisodes :many
WITH totals AS (
    SELECT
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM unmatched_stop_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND opened_at < @to_time::timestamptz
      AND (closed_at IS NULL OR closed_at >= @from_time::timestamptz)
),
closed_counts AS (
    SELECT
        close_reason,
        count(*) AS closed
    FROM unmatched_stop_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY close_reason
)
SELECT
    totals.opened,
    totals.open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM totals
LEFT JOIN closed_counts ON TRUE;

-- name: CountUnmatchedTripEpisodes :many
WITH totals AS (
    SELECT
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM unmatched_trip_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND opened_at < @to_time::timestamptz
      AND (closed_at IS NULL OR closed_at >= @from_time::timestamptz)
),
closed_counts AS (
    SELECT
        close_reason,
        count(*) AS closed
    FROM unmatched_trip_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY close_reason
)
SELECT
    totals.opened,
    totals.open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM totals
LEFT JOIN closed_counts ON TRUE;

-- name: CountVehicleAnomalyEpisodes :many
WITH totals AS (
    SELECT
        anomaly_code,
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM vehicle_anomaly_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND opened_at < @to_time::timestamptz
      AND (closed_at IS NULL OR closed_at >= @from_time::timestamptz)
    GROUP BY anomaly_code
),
closed_counts AS (
    SELECT
        anomaly_code,
        close_reason,
        count(*) AS closed
    FROM vehicle_anomaly_episode_view
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR agency_id = sqlc.narg('agency_id')::bigint)
      AND closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY anomaly_code, close_reason
)
SELECT
    vat.anomaly_code,
    COALESCE(totals.opened, 0)::bigint AS opened,
    COALESCE(totals.open_at_to, 0)::bigint AS open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM vehicle_anomaly_type vat
LEFT JOIN totals ON totals.anomaly_code = vat.anomaly_code
LEFT JOIN closed_counts ON closed_counts.anomaly_code = vat.anomaly_code;


-- name: CountUnmatchedStopEpisodesGrouped :many
WITH filtered AS (
    SELECT
        CASE sqlc.arg('dimension')::text
            WHEN 'agency' THEN e.agency_id::text
            WHEN 'opening_response' THEN e.opening_response_id::text
            WHEN 'closing_response' THEN e.closing_response_id::text
            WHEN 'static_found' THEN e.static_found::text
            WHEN 'gtfs_static_version' THEN e.gtfs_static_version_id::text
            WHEN 'cluster' THEN
                CASE
                    WHEN sca.cell_id IS NOT NULL THEN
                        json_build_object('cell_id', sca.cell_id, 'root_station_id', sca.root_station_id)::text
                    ELSE NULL
                END
            WHEN 'recurrence' THEN
                CASE
                    WHEN e.previous_episode_count > 0 THEN 'has_previous_episode'
                    ELSE 'no_previous_episode'
                END
            WHEN 'entity' THEN
                json_build_object('reported_stop_id', e.reported_stop_id, 'stop_id', e.stop_id, 'agency_id', e.agency_id)::text
            WHEN 'opened_hour' THEN
                to_char(date_trunc('hour', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            WHEN 'opened_day' THEN
                to_char(date_trunc('day', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            ELSE NULL::text
        END AS group_key,
        e.opened_at,
        e.closed_at,
        e.close_reason
    FROM unmatched_stop_episode_view e
    LEFT JOIN stop_cluster_assignment sca
        ON sca.stop_metadata_version_id = e.stop_metadata_version_id
       AND sca.scheme_version = sqlc.narg('scheme_version')::text
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR e.agency_id = sqlc.narg('agency_id')::bigint)
      AND e.opened_at < @to_time::timestamptz
      AND (e.closed_at IS NULL OR e.closed_at >= @from_time::timestamptz)
),
totals AS (
    SELECT
        group_key,
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM filtered
    GROUP BY group_key
),
closed_counts AS (
    SELECT
        group_key,
        close_reason,
        count(*) AS closed
    FROM filtered
    WHERE closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY group_key, close_reason
)
SELECT
    totals.group_key,
    totals.opened,
    totals.open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM totals
LEFT JOIN closed_counts ON totals.group_key IS NOT DISTINCT FROM closed_counts.group_key;

-- name: CountUnmatchedTripEpisodesGrouped :many
WITH filtered AS (
    SELECT
        CASE sqlc.arg('dimension')::text
            WHEN 'agency' THEN e.agency_id::text
            WHEN 'opening_response' THEN e.opening_response_id::text
            WHEN 'closing_response' THEN e.closing_response_id::text
            WHEN 'static_found' THEN e.static_found::text
            WHEN 'gtfs_static_version' THEN e.gtfs_static_version_id::text
            WHEN 'route_id' THEN e.route_id
            WHEN 'recurrence' THEN
                CASE
                    WHEN e.previous_episode_count > 0 THEN 'has_previous_episode'
                    ELSE 'no_previous_episode'
                END
            WHEN 'entity' THEN
                json_build_object('reported_trip_id', e.reported_trip_id, 'trip_id', e.trip_id, 'agency_id', e.agency_id)::text
            WHEN 'opened_hour' THEN
                to_char(date_trunc('hour', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            WHEN 'opened_day' THEN
                to_char(date_trunc('day', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            ELSE NULL::text
        END AS group_key,
        e.opened_at,
        e.closed_at,
        e.close_reason
    FROM unmatched_trip_episode_view e
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR e.agency_id = sqlc.narg('agency_id')::bigint)
      AND e.opened_at < @to_time::timestamptz
      AND (e.closed_at IS NULL OR e.closed_at >= @from_time::timestamptz)
),
totals AS (
    SELECT
        group_key,
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM filtered
    GROUP BY group_key
),
closed_counts AS (
    SELECT
        group_key,
        close_reason,
        count(*) AS closed
    FROM filtered
    WHERE closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY group_key, close_reason
)
SELECT
    totals.group_key,
    totals.opened,
    totals.open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM totals
LEFT JOIN closed_counts ON totals.group_key IS NOT DISTINCT FROM closed_counts.group_key;

-- name: CountVehicleAnomalyEpisodesGrouped :many
WITH filtered AS (
    SELECT
        e.anomaly_code,
        CASE sqlc.arg('dimension')::text
            WHEN 'agency' THEN e.agency_id::text
            WHEN 'endpoint' THEN e.endpoint_id::text
            WHEN 'opening_response' THEN e.opening_response_id::text
            WHEN 'closing_response' THEN e.closing_response_id::text
            WHEN 'gtfs_static_version' THEN e.gtfs_static_version_id::text
            WHEN 'route_id' THEN e.route_id
            WHEN 'rule_version' THEN e.rule_version_id::text
            WHEN 'bounding_box_version' THEN e.bounding_box_version_id::text
            WHEN 'position_validation_reason' THEN e.position_validation_reason
            WHEN 'attribution_reason' THEN e.attribution_reason
            WHEN 'expected_schedule_state' THEN e.expected_schedule_state
            WHEN 'recurrence' THEN
                CASE
                    WHEN e.previous_episode_count > 0 THEN 'has_previous_episode'
                    ELSE 'no_previous_episode'
                END
            WHEN 'entity' THEN
                json_build_object('vehicle_entity_id', e.vehicle_entity_id, 'vehicle_id', e.vehicle_id, 'endpoint_id', e.endpoint_id)::text
            WHEN 'opened_hour' THEN
                to_char(date_trunc('hour', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            WHEN 'opened_day' THEN
                to_char(date_trunc('day', e.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
            ELSE NULL::text
        END AS group_key,
        e.opened_at,
        e.closed_at,
        e.close_reason
    FROM vehicle_anomaly_episode_view e
    WHERE (sqlc.narg('server_id')::bigint IS NULL OR e.server_id = sqlc.narg('server_id')::bigint)
      AND (sqlc.narg('agency_id')::bigint IS NULL OR e.agency_id = sqlc.narg('agency_id')::bigint)
      AND (sqlc.narg('anomaly_code')::text IS NULL OR e.anomaly_code = sqlc.narg('anomaly_code')::text)
      AND e.opened_at < @to_time::timestamptz
      AND (e.closed_at IS NULL OR e.closed_at >= @from_time::timestamptz)
),
totals AS (
    SELECT
        anomaly_code,
        group_key,
        count(*) FILTER (WHERE opened_at >= @from_time::timestamptz AND opened_at < @to_time::timestamptz) AS opened,
        count(*) FILTER (WHERE opened_at < @to_time::timestamptz AND (closed_at IS NULL OR closed_at >= @to_time::timestamptz)) AS open_at_to
    FROM filtered
    GROUP BY anomaly_code, group_key
),
closed_counts AS (
    SELECT
        anomaly_code,
        group_key,
        close_reason,
        count(*) AS closed
    FROM filtered
    WHERE closed_at >= @from_time::timestamptz AND closed_at < @to_time::timestamptz
      AND close_reason IS NOT NULL
    GROUP BY anomaly_code, group_key, close_reason
)
SELECT
    totals.anomaly_code,
    totals.group_key,
    totals.opened,
    totals.open_at_to,
    closed_counts.close_reason,
    COALESCE(closed_counts.closed, 0)::bigint AS closed
FROM totals
LEFT JOIN closed_counts
    ON totals.anomaly_code = closed_counts.anomaly_code
   AND totals.group_key IS NOT DISTINCT FROM closed_counts.group_key;
