-- name: GetVehicleAnomalyEpisode :one
SELECT sqlc.embed(v), opening_link.vehicle_position_evidence_id AS opening_evidence_id
FROM vehicle_anomaly_episode_view v
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = v.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
WHERE v.vehicle_anomaly_episode_id = $1;

-- name: ListVehicleAnomalyEpisodes :many
-- One page of vehicle anomaly episodes, newest first. E24 uses every filter;
-- E23 sets vehicle_entity_id, with an optional window that has no default or maximum.
SELECT sqlc.embed(v), opening_link.vehicle_position_evidence_id AS opening_evidence_id
FROM vehicle_anomaly_episode_view v
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = v.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
WHERE (sqlc.narg('server_id')::bigint IS NULL OR v.server_id = sqlc.narg('server_id')::bigint)
  AND (
      CASE
          WHEN sqlc.narg('agency_none')::boolean IS TRUE THEN v.agency_id IS NULL
          WHEN sqlc.narg('agency_id')::bigint IS NOT NULL THEN v.agency_id = sqlc.narg('agency_id')::bigint
          ELSE TRUE
      END
  )
  AND (sqlc.narg('endpoint_id')::bigint IS NULL OR v.endpoint_id = sqlc.narg('endpoint_id')::bigint)
  AND (sqlc.narg('vehicle_entity_id')::bigint IS NULL OR v.vehicle_entity_id = sqlc.narg('vehicle_entity_id')::bigint)
  AND (sqlc.narg('external_vehicle_id')::text IS NULL OR v.vehicle_id = sqlc.narg('external_vehicle_id')::text)
  AND (sqlc.narg('anomaly_code')::text IS NULL OR v.anomaly_code = sqlc.narg('anomaly_code')::text)
  AND (sqlc.narg('status')::text IS NULL
       OR (sqlc.narg('status')::text = 'open' AND v.closing_response_id IS NULL)
       OR (sqlc.narg('status')::text = 'closed' AND v.closing_response_id IS NOT NULL))
  AND (sqlc.narg('active_to')::timestamptz IS NULL OR v.opened_at < sqlc.narg('active_to')::timestamptz)
  AND (sqlc.narg('active_from')::timestamptz IS NULL OR v.closed_at IS NULL
       OR v.closed_at >= sqlc.narg('active_from')::timestamptz)
  AND (sqlc.narg('opened_from')::timestamptz IS NULL OR v.opened_at >= sqlc.narg('opened_from')::timestamptz)
  AND (sqlc.narg('opened_to')::timestamptz IS NULL OR v.opened_at < sqlc.narg('opened_to')::timestamptz)
  AND (sqlc.narg('open_at')::timestamptz IS NULL
       OR (v.opened_at <= sqlc.narg('open_at')::timestamptz
           AND (v.closed_at IS NULL OR v.closed_at > sqlc.narg('open_at')::timestamptz)))
  AND (sqlc.narg('close_reason')::text IS NULL OR v.close_reason = sqlc.narg('close_reason')::text)
  AND (sqlc.narg('opening_response_id')::bigint IS NULL OR v.opening_response_id = sqlc.narg('opening_response_id')::bigint)
  AND (sqlc.narg('closing_response_id')::bigint IS NULL OR v.closing_response_id = sqlc.narg('closing_response_id')::bigint)
  AND (sqlc.narg('gtfs_static_version_id')::bigint IS NULL OR v.gtfs_static_version_id = sqlc.narg('gtfs_static_version_id')::bigint)
  AND (
      CASE
          WHEN sqlc.narg('recurrence')::text = 'has_previous_episode' THEN v.previous_episode_count > 0
          WHEN sqlc.narg('recurrence')::text = 'no_previous_episode' THEN v.previous_episode_count = 0
          ELSE TRUE
      END
  )
  AND (sqlc.narg('route_id')::text IS NULL OR v.route_id = sqlc.narg('route_id')::text)
  AND (sqlc.narg('rule_version_id')::bigint IS NULL OR v.rule_version_id = sqlc.narg('rule_version_id')::bigint)
  AND (sqlc.narg('bounding_box_version_id')::bigint IS NULL OR v.bounding_box_version_id = sqlc.narg('bounding_box_version_id')::bigint)
  AND (sqlc.narg('position_validation_reason')::text IS NULL OR v.position_validation_reason = sqlc.narg('position_validation_reason')::text)
  AND (sqlc.narg('attribution_reason')::text IS NULL OR v.attribution_reason = sqlc.narg('attribution_reason')::text)
  AND (sqlc.narg('expected_schedule_state')::text IS NULL OR v.expected_schedule_state = sqlc.narg('expected_schedule_state')::text)
  AND (sqlc.narg('after_id')::bigint IS NULL OR v.vehicle_anomaly_episode_id < sqlc.narg('after_id')::bigint)
ORDER BY v.vehicle_anomaly_episode_id DESC
LIMIT sqlc.arg('page_limit');

-- name: ListVehicleAnomalyEpisodeEvidence :many
-- The evidence rows of the given episodes, each with its role, in observed
-- order (E26). One query serves the endpoint (one episode) and the include
-- loader (many episodes). observed_at and incrementality come from each row's
-- response; the other columns are the E27 fields.
SELECT l.vehicle_anomaly_episode_id,
       l.evidence_role,
       e.vehicle_position_evidence_id,
       e.vehicle_position_response_id,
       r.observed_at,
       r.incrementality,
       e.vehicle_entity_id,
       e.feed_entity_id,
       e.agency_id,
       e.attribution_reason,
       e.vehicle_source_time,
       e.latitude,
       e.longitude,
       e.position_valid,
       e.position_validation_reason,
       e.reported_speed_mps,
       e.computed_speed_mps,
       e.speed_discrepancy_ratio,
       e.current_status,
       e.trip_id,
       e.route_id,
       e.trip_start_date,
       e.trip_start_time_seconds,
       e.schedule_relationship,
       e.expected_schedule_state,
       e.expected_schedule_reason,
       e.expected_window_start,
       e.expected_window_end,
       e.gtfs_static_version_id,
       e.bounding_box_version_id
FROM vehicle_anomaly_episode_evidence AS l
JOIN vehicle_position_evidence AS e ON e.vehicle_position_evidence_id = l.vehicle_position_evidence_id
JOIN vehicle_position_response AS r ON r.vehicle_position_response_id = e.vehicle_position_response_id
WHERE l.vehicle_anomaly_episode_id = ANY(sqlc.arg('vehicle_anomaly_episode_ids')::bigint[])
ORDER BY l.vehicle_anomaly_episode_id, r.observed_at, e.vehicle_position_evidence_id;
