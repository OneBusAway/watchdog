-- name: GetVehiclePositionEvidence :many
-- The vehicle position evidence rows with the given IDs, with the observed_at
-- and incrementality of each row's response (E27). One query serves the endpoint
-- (one ID) and the include loader (many IDs).
SELECT e.vehicle_position_evidence_id,
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
FROM vehicle_position_evidence AS e
JOIN vehicle_position_response AS r ON r.vehicle_position_response_id = e.vehicle_position_response_id
WHERE e.vehicle_position_evidence_id = ANY(sqlc.arg('vehicle_position_evidence_ids')::bigint[])
ORDER BY e.vehicle_position_evidence_id;
