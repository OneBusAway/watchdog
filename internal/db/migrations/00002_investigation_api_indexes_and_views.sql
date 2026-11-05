-- Indexes and episode views for the investigation API.
--
-- The views define each episode's derived fields once, so filters, counts and
-- single-episode reads agree. Their columns and rules are documented in the
-- "Views for the investigation API" section of
-- docs/historical-investigation-schema.md, and decisions D28 and D29 in
-- docs/investigation-api.md. Keep this file and those documents in step.
--
-- The latest static context and the recurrence columns use LATERAL subqueries,
-- not window functions. A window function partitioned by identity would stop
-- PostgreSQL from applying filters such as agency_id before computing it.

-- +goose Up

-- List endpoints filter by agency or anomaly type and page by episode ID
-- descending. A vehicle episode's agency comes from its opening evidence.
CREATE INDEX unmatched_stop_episode_agency_id_desc
    ON unmatched_stop_episode (agency_id, unmatched_stop_episode_id DESC);
CREATE INDEX unmatched_trip_episode_agency_id_desc
    ON unmatched_trip_episode (agency_id, unmatched_trip_episode_id DESC);
CREATE INDEX vehicle_anomaly_episode_type_id_desc
    ON vehicle_anomaly_episode (anomaly_type_id, vehicle_anomaly_episode_id DESC);
CREATE INDEX vehicle_position_evidence_agency_response
    ON vehicle_position_evidence (agency_id, vehicle_position_response_id);

-- "Earlier" episodes of the same identity compare (opened_at, episode ID), so
-- two episodes that opened at the same instant still get distinct counts.
-- previous_closed_at is the closed_at of the latest of those earlier episodes.

CREATE VIEW unmatched_stop_episode_view AS
SELECT
    e.unmatched_stop_episode_id,
    e.reported_stop_id,
    e.agency_id,
    e.first_response_agency_id,
    e.closing_response_agency_id,
    e.last_reported_at,
    e.close_reason,
    a.server_id,
    opening.observed_at       AS opened_at,
    closing.observed_at       AS closed_at,
    opening.response_id       AS opening_response_id,
    closing.response_id       AS closing_response_id,
    s.oba_stop_id             AS stop_id,
    v.stop_metadata_version_id,
    v.static_stop_found       AS static_found,
    v.gtfs_static_version_id,
    recurrence.previous_episode_count,
    previous.previous_closed_at
FROM unmatched_stop_episode e
JOIN agency a ON a.agency_id = e.agency_id
JOIN reported_stop s ON s.reported_stop_id = e.reported_stop_id
JOIN onebusaway_metrics_response_agency opening_agency
    ON opening_agency.response_agency_id = e.first_response_agency_id
JOIN onebusaway_metrics_response opening ON opening.response_id = opening_agency.response_id
LEFT JOIN onebusaway_metrics_response_agency closing_agency
    ON closing_agency.response_agency_id = e.closing_response_agency_id
LEFT JOIN onebusaway_metrics_response closing ON closing.response_id = closing_agency.response_id
-- Latest static context: the linked metadata row whose static version was
-- published last. The LATERAL picks only its ID; the row itself comes from a
-- plain LEFT JOIN so sqlc marks its columns nullable (no metadata linked yet).
LEFT JOIN LATERAL (
    SELECT lv.stop_metadata_version_id
    FROM unmatched_stop_episode_metadata m
    JOIN stop_metadata_version lv ON lv.stop_metadata_version_id = m.stop_metadata_version_id
    JOIN gtfs_static_version g ON g.gtfs_static_version_id = lv.gtfs_static_version_id
    WHERE m.unmatched_stop_episode_id = e.unmatched_stop_episode_id
    ORDER BY g.published_at DESC, g.gtfs_static_version_id DESC
    LIMIT 1
) latest ON TRUE
LEFT JOIN stop_metadata_version v ON v.stop_metadata_version_id = latest.stop_metadata_version_id
CROSS JOIN LATERAL (
    SELECT count(*) AS previous_episode_count
    FROM unmatched_stop_episode p
    JOIN onebusaway_metrics_response_agency p_opening_agency
        ON p_opening_agency.response_agency_id = p.first_response_agency_id
    JOIN onebusaway_metrics_response p_opening ON p_opening.response_id = p_opening_agency.response_id
    WHERE p.reported_stop_id = e.reported_stop_id
      AND (p_opening.observed_at, p.unmatched_stop_episode_id)
          < (opening.observed_at, e.unmatched_stop_episode_id)
) recurrence
LEFT JOIN LATERAL (
    SELECT p_closing.observed_at AS previous_closed_at
    FROM unmatched_stop_episode p
    JOIN onebusaway_metrics_response_agency p_opening_agency
        ON p_opening_agency.response_agency_id = p.first_response_agency_id
    JOIN onebusaway_metrics_response p_opening ON p_opening.response_id = p_opening_agency.response_id
    LEFT JOIN onebusaway_metrics_response_agency p_closing_agency
        ON p_closing_agency.response_agency_id = p.closing_response_agency_id
    LEFT JOIN onebusaway_metrics_response p_closing ON p_closing.response_id = p_closing_agency.response_id
    WHERE p.reported_stop_id = e.reported_stop_id
      AND (p_opening.observed_at, p.unmatched_stop_episode_id)
          < (opening.observed_at, e.unmatched_stop_episode_id)
    ORDER BY p_opening.observed_at DESC, p.unmatched_stop_episode_id DESC
    LIMIT 1
) previous ON TRUE;

CREATE VIEW unmatched_trip_episode_view AS
SELECT
    e.unmatched_trip_episode_id,
    e.reported_trip_id,
    e.agency_id,
    e.first_response_agency_id,
    e.closing_response_agency_id,
    e.last_reported_at,
    e.close_reason,
    a.server_id,
    opening.observed_at       AS opened_at,
    closing.observed_at       AS closed_at,
    opening.response_id       AS opening_response_id,
    closing.response_id       AS closing_response_id,
    r.oba_trip_id             AS trip_id,
    v.trip_metadata_version_id,
    v.static_trip_found       AS static_found,
    v.gtfs_static_version_id,
    v.route_id,
    v.service_id,
    v.block_id,
    recurrence.previous_episode_count,
    previous.previous_closed_at
FROM unmatched_trip_episode e
JOIN agency a ON a.agency_id = e.agency_id
JOIN reported_trip r ON r.reported_trip_id = e.reported_trip_id
JOIN onebusaway_metrics_response_agency opening_agency
    ON opening_agency.response_agency_id = e.first_response_agency_id
JOIN onebusaway_metrics_response opening ON opening.response_id = opening_agency.response_id
LEFT JOIN onebusaway_metrics_response_agency closing_agency
    ON closing_agency.response_agency_id = e.closing_response_agency_id
LEFT JOIN onebusaway_metrics_response closing ON closing.response_id = closing_agency.response_id
LEFT JOIN LATERAL (
    SELECT lv.trip_metadata_version_id
    FROM unmatched_trip_episode_metadata m
    JOIN trip_metadata_version lv ON lv.trip_metadata_version_id = m.trip_metadata_version_id
    JOIN gtfs_static_version g ON g.gtfs_static_version_id = lv.gtfs_static_version_id
    WHERE m.unmatched_trip_episode_id = e.unmatched_trip_episode_id
    ORDER BY g.published_at DESC, g.gtfs_static_version_id DESC
    LIMIT 1
) latest ON TRUE
LEFT JOIN trip_metadata_version v ON v.trip_metadata_version_id = latest.trip_metadata_version_id
CROSS JOIN LATERAL (
    SELECT count(*) AS previous_episode_count
    FROM unmatched_trip_episode p
    JOIN onebusaway_metrics_response_agency p_opening_agency
        ON p_opening_agency.response_agency_id = p.first_response_agency_id
    JOIN onebusaway_metrics_response p_opening ON p_opening.response_id = p_opening_agency.response_id
    WHERE p.reported_trip_id = e.reported_trip_id
      AND (p_opening.observed_at, p.unmatched_trip_episode_id)
          < (opening.observed_at, e.unmatched_trip_episode_id)
) recurrence
LEFT JOIN LATERAL (
    SELECT p_closing.observed_at AS previous_closed_at
    FROM unmatched_trip_episode p
    JOIN onebusaway_metrics_response_agency p_opening_agency
        ON p_opening_agency.response_agency_id = p.first_response_agency_id
    JOIN onebusaway_metrics_response p_opening ON p_opening.response_id = p_opening_agency.response_id
    LEFT JOIN onebusaway_metrics_response_agency p_closing_agency
        ON p_closing_agency.response_agency_id = p.closing_response_agency_id
    LEFT JOIN onebusaway_metrics_response p_closing ON p_closing.response_id = p_closing_agency.response_id
    WHERE p.reported_trip_id = e.reported_trip_id
      AND (p_opening.observed_at, p.unmatched_trip_episode_id)
          < (opening.observed_at, e.unmatched_trip_episode_id)
    ORDER BY p_opening.observed_at DESC, p.unmatched_trip_episode_id DESC
    LIMIT 1
) previous ON TRUE;

CREATE VIEW vehicle_anomaly_episode_view AS
SELECT
    e.vehicle_anomaly_episode_id,
    e.vehicle_entity_id,
    e.anomaly_type_id,
    e.rule_version_id,
    e.opening_response_id,
    e.closing_response_id,
    e.close_reason,
    endpoint.server_id,
    opening.observed_at       AS opened_at,
    closing.observed_at       AS closed_at,
    vehicle.external_vehicle_id AS vehicle_id,
    vehicle.endpoint_id,
    t.anomaly_code,
    -- Opening evidence. agency_id is null when the vehicle was not attributed.
    evidence.agency_id,
    evidence.attribution_reason,
    evidence.position_validation_reason,
    evidence.expected_schedule_state,
    evidence.route_id,
    evidence.gtfs_static_version_id,
    evidence.bounding_box_version_id,
    recurrence.previous_episode_count,
    previous.previous_closed_at
FROM vehicle_anomaly_episode e
JOIN vehicle_entity vehicle ON vehicle.vehicle_entity_id = e.vehicle_entity_id
JOIN vehicle_position_endpoint endpoint ON endpoint.endpoint_id = vehicle.endpoint_id
JOIN vehicle_anomaly_type t ON t.anomaly_type_id = e.anomaly_type_id
JOIN vehicle_position_response opening ON opening.vehicle_position_response_id = e.opening_response_id
LEFT JOIN vehicle_position_response closing ON closing.vehicle_position_response_id = e.closing_response_id
-- A partial unique index allows at most one opening link per episode.
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = e.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
LEFT JOIN vehicle_position_evidence evidence
    ON evidence.vehicle_position_evidence_id = opening_link.vehicle_position_evidence_id
CROSS JOIN LATERAL (
    SELECT count(*) AS previous_episode_count
    FROM vehicle_anomaly_episode p
    JOIN vehicle_position_response p_opening
        ON p_opening.vehicle_position_response_id = p.opening_response_id
    WHERE p.vehicle_entity_id = e.vehicle_entity_id
      AND p.anomaly_type_id = e.anomaly_type_id
      AND (p_opening.observed_at, p.vehicle_anomaly_episode_id)
          < (opening.observed_at, e.vehicle_anomaly_episode_id)
) recurrence
LEFT JOIN LATERAL (
    SELECT p_closing.observed_at AS previous_closed_at
    FROM vehicle_anomaly_episode p
    JOIN vehicle_position_response p_opening
        ON p_opening.vehicle_position_response_id = p.opening_response_id
    LEFT JOIN vehicle_position_response p_closing
        ON p_closing.vehicle_position_response_id = p.closing_response_id
    WHERE p.vehicle_entity_id = e.vehicle_entity_id
      AND p.anomaly_type_id = e.anomaly_type_id
      AND (p_opening.observed_at, p.vehicle_anomaly_episode_id)
          < (opening.observed_at, e.vehicle_anomaly_episode_id)
    ORDER BY p_opening.observed_at DESC, p.vehicle_anomaly_episode_id DESC
    LIMIT 1
) previous ON TRUE;

-- +goose Down
DROP VIEW vehicle_anomaly_episode_view;
DROP VIEW unmatched_trip_episode_view;
DROP VIEW unmatched_stop_episode_view;

DROP INDEX vehicle_position_evidence_agency_response;
DROP INDEX vehicle_anomaly_episode_type_id_desc;
DROP INDEX unmatched_trip_episode_agency_id_desc;
DROP INDEX unmatched_stop_episode_agency_id_desc;
