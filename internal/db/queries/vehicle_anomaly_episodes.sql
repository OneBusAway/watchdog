-- name: GetVehicleAnomalyEpisode :one
SELECT sqlc.embed(v), opening_link.vehicle_position_evidence_id AS opening_evidence_id
FROM vehicle_anomaly_episode_view v
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = v.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
WHERE v.vehicle_anomaly_episode_id = $1;

-- name: ListVehicleAnomalyEpisodes :many
-- One page of vehicle anomaly episodes, newest first. E24 uses every filter;
-- E23 sets vehicle_entity_id and leaves the window unbounded.
SELECT sqlc.embed(v), opening_link.vehicle_position_evidence_id AS opening_evidence_id
FROM vehicle_anomaly_episode_view v
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = v.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
WHERE (sqlc.narg('server_id')::bigint IS NULL OR v.server_id = sqlc.narg('server_id')::bigint)
  AND (sqlc.narg('agency_id')::bigint IS NULL OR v.agency_id = sqlc.narg('agency_id')::bigint)
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
  AND (sqlc.narg('close_reason')::text IS NULL OR v.close_reason = sqlc.narg('close_reason')::text)
  AND (sqlc.narg('after_id')::bigint IS NULL OR v.vehicle_anomaly_episode_id < sqlc.narg('after_id')::bigint)
ORDER BY v.vehicle_anomaly_episode_id DESC
LIMIT sqlc.arg('page_limit');
