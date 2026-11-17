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
