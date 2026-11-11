-- name: GetVehiclePositionEvidence :one
-- One vehicle-position evidence record by ID, joined with its parent response
-- to provide observed_at and endpoint_id for caching and links.
SELECT
    e.vehicle_position_evidence_id,
    e.vehicle_position_response_id,
    r.endpoint_id,
    r.observed_at,
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
FROM vehicle_position_evidence e
JOIN vehicle_position_response r ON r.vehicle_position_response_id = e.vehicle_position_response_id
WHERE e.vehicle_position_evidence_id = $1;
