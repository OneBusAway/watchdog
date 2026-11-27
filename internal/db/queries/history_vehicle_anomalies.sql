-- name: ListVehicleAnomalyTypeIDs :many
-- The ID of every anomaly type, read once before the first episode opens
-- after startup.
SELECT anomaly_type_id, anomaly_code
FROM vehicle_anomaly_type
ORDER BY anomaly_type_id;

-- name: ListNewestVehicleAnomalyRuleVersions :many
-- The rule version currently in effect for each anomaly type that has one.
SELECT DISTINCT ON (anomaly_type_id)
    rule_version_id, anomaly_type_id, rule_logic_version,
    stale_age_threshold_seconds, reported_speed_threshold_mps
FROM vehicle_anomaly_rule_version
ORDER BY anomaly_type_id, effective_at DESC;

-- name: InsertVehicleAnomalyRuleVersion :one
INSERT INTO vehicle_anomaly_rule_version (
    anomaly_type_id, rule_logic_version, version_name, watchdog_build_version,
    effective_at, stale_age_threshold_seconds
)
VALUES (
    @anomaly_type_id, @rule_logic_version, @version_name, @watchdog_build_version,
    @effective_at, @stale_age_threshold_seconds
)
RETURNING rule_version_id;

-- name: ListOpenVehicleAnomalyEpisodesForServer :many
-- The one read outside a write: the first recording for a server after
-- startup loads its open episodes into memory, each with the trip of its most
-- recent evidence so a later trip change can be detected. An agency-mode
-- entry passes its agency: entries that share a base URL can share a feed
-- URL, each with a snapshot filtered to its own agency, so an entry loads
-- only the episodes its own agency opened.
SELECT
    e.vehicle_anomaly_episode_id,
    t.anomaly_code,
    ep.normalized_vehicle_position_url,
    v.external_vehicle_id,
    latest.trip_id,
    latest.trip_start_date
FROM vehicle_anomaly_episode e
JOIN vehicle_anomaly_type t ON t.anomaly_type_id = e.anomaly_type_id
JOIN vehicle_entity v ON v.vehicle_entity_id = e.vehicle_entity_id
JOIN vehicle_position_endpoint ep ON ep.endpoint_id = v.endpoint_id
JOIN onebusaway_server srv ON srv.server_id = ep.server_id
LEFT JOIN LATERAL (
    SELECT pe.trip_id, pe.trip_start_date
    FROM vehicle_anomaly_episode_evidence link
    JOIN vehicle_position_evidence pe
      ON pe.vehicle_position_evidence_id = link.vehicle_position_evidence_id
    WHERE link.vehicle_anomaly_episode_id = e.vehicle_anomaly_episode_id
    ORDER BY pe.vehicle_position_evidence_id DESC
    LIMIT 1
) latest ON TRUE
WHERE srv.normalized_base_url = @normalized_base_url
  AND e.closing_response_id IS NULL
  AND (sqlc.narg('external_agency_id')::text IS NULL OR EXISTS (
      SELECT 1
      FROM vehicle_anomaly_episode_evidence opening
      JOIN vehicle_position_evidence oe
        ON oe.vehicle_position_evidence_id = opening.vehicle_position_evidence_id
      JOIN agency a ON a.agency_id = oe.agency_id
      WHERE opening.vehicle_anomaly_episode_id = e.vehicle_anomaly_episode_id
        AND opening.evidence_role = 'opening'
        AND a.external_agency_id = sqlc.narg('external_agency_id')::text
  ));

-- name: InsertVehiclePositionResponse :one
INSERT INTO vehicle_position_response (
    endpoint_id, observed_at, source_header_time, incrementality, payload_sha256
)
VALUES (@endpoint_id, @observed_at, @source_header_time, @incrementality, @payload_sha256)
RETURNING vehicle_position_response_id;

-- name: InsertVehiclePositionEvidence :one
INSERT INTO vehicle_position_evidence (
    vehicle_position_response_id, vehicle_entity_id, feed_entity_id,
    agency_id, attribution_reason, vehicle_source_time,
    latitude, longitude, position_valid, position_validation_reason,
    reported_speed_mps, computed_speed_mps, speed_discrepancy_ratio,
    current_status, trip_id, route_id, trip_start_date, trip_start_time_seconds,
    schedule_relationship, expected_schedule_state, expected_schedule_reason,
    expected_window_start, expected_window_end,
    gtfs_static_version_id, bounding_box_version_id
)
VALUES (
    @vehicle_position_response_id, @vehicle_entity_id, @feed_entity_id,
    @agency_id, @attribution_reason, @vehicle_source_time,
    @latitude, @longitude, @position_valid, @position_validation_reason,
    @reported_speed_mps, @computed_speed_mps, @speed_discrepancy_ratio,
    @current_status, @trip_id, @route_id, @trip_start_date, @trip_start_time_seconds,
    @schedule_relationship, @expected_schedule_state, @expected_schedule_reason,
    @expected_window_start, @expected_window_end,
    @gtfs_static_version_id, @bounding_box_version_id
)
RETURNING vehicle_position_evidence_id;

-- name: InsertVehicleAnomalyEpisode :one
INSERT INTO vehicle_anomaly_episode (
    vehicle_entity_id, anomaly_type_id, rule_version_id, opening_response_id
)
VALUES (@vehicle_entity_id, @anomaly_type_id, @rule_version_id, @opening_response_id)
RETURNING vehicle_anomaly_episode_id;

-- name: LinkVehicleAnomalyEpisodeEvidence :exec
INSERT INTO vehicle_anomaly_episode_evidence (
    vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role
)
VALUES (@vehicle_anomaly_episode_id, @vehicle_position_evidence_id, @evidence_role);

-- name: CloseVehicleAnomalyEpisode :execrows
-- Reports the rows changed, so a close of an episode that is no longer open
-- fails the write instead of passing silently.
UPDATE vehicle_anomaly_episode
SET closing_response_id = @closing_response_id::bigint,
    close_reason = @close_reason::text
WHERE vehicle_anomaly_episode_id = @vehicle_anomaly_episode_id
  AND closing_response_id IS NULL;
