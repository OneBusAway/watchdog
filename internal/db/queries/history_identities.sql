-- Identity upserts used by internal/history. Each query returns the ID of the
-- row for its natural key, creating the row when it does not exist yet.
--
-- The pattern is an INSERT ... ON CONFLICT inside a CTE, followed by a SELECT
-- of the existing row. When the row already exists and nothing changed, the
-- INSERT returns nothing and the SELECT supplies the ID, so an unchanged
-- identity is read but never rewritten. UNION ALL keeps the CTE's row first,
-- and LIMIT 1 drops the old-snapshot copy when the CTE did update a row.

-- name: EnsureOneBusAwayServer :one
-- display_name follows the configuration: it is updated when it changes.
WITH upserted AS (
    INSERT INTO onebusaway_server (normalized_base_url, display_name)
    VALUES (@normalized_base_url, @display_name)
    ON CONFLICT (normalized_base_url) DO UPDATE
        SET display_name = EXCLUDED.display_name
        WHERE onebusaway_server.display_name IS DISTINCT FROM EXCLUDED.display_name
    RETURNING server_id
)
SELECT server_id FROM upserted
UNION ALL
SELECT server_id FROM onebusaway_server WHERE normalized_base_url = @normalized_base_url
LIMIT 1;

-- name: EnsureAgency :one
-- current_name is updated when a name is given and differs, and kept when the
-- caller has no name (NULL).
WITH upserted AS (
    INSERT INTO agency (server_id, external_agency_id, current_name)
    VALUES (@server_id, @external_agency_id, sqlc.narg('current_name'))
    ON CONFLICT (server_id, external_agency_id) DO UPDATE
        SET current_name = EXCLUDED.current_name
        WHERE EXCLUDED.current_name IS NOT NULL
          AND agency.current_name IS DISTINCT FROM EXCLUDED.current_name
    RETURNING agency_id
)
SELECT agency_id FROM upserted
UNION ALL
SELECT agency_id FROM agency
WHERE server_id = @server_id AND external_agency_id = @external_agency_id
LIMIT 1;

-- name: EnsureVehiclePositionEndpoint :one
WITH inserted AS (
    INSERT INTO vehicle_position_endpoint (server_id, normalized_vehicle_position_url)
    VALUES (@server_id, @normalized_vehicle_position_url)
    ON CONFLICT (server_id, normalized_vehicle_position_url) DO NOTHING
    RETURNING endpoint_id
)
SELECT endpoint_id FROM inserted
UNION ALL
SELECT endpoint_id FROM vehicle_position_endpoint
WHERE server_id = @server_id AND normalized_vehicle_position_url = @normalized_vehicle_position_url
LIMIT 1;

-- name: EnsureStaticGTFSSource :one
WITH inserted AS (
    INSERT INTO static_gtfs_source (server_id, normalized_source_url)
    VALUES (@server_id, @normalized_source_url)
    ON CONFLICT (server_id, normalized_source_url) DO NOTHING
    RETURNING source_id
)
SELECT source_id FROM inserted
UNION ALL
SELECT source_id FROM static_gtfs_source
WHERE server_id = @server_id AND normalized_source_url = @normalized_source_url
LIMIT 1;

-- name: EnsureVehicleEntity :one
WITH inserted AS (
    INSERT INTO vehicle_entity (endpoint_id, external_vehicle_id)
    VALUES (@endpoint_id, @external_vehicle_id)
    ON CONFLICT (endpoint_id, external_vehicle_id) DO NOTHING
    RETURNING vehicle_entity_id
)
SELECT vehicle_entity_id FROM inserted
UNION ALL
SELECT vehicle_entity_id FROM vehicle_entity
WHERE endpoint_id = @endpoint_id AND external_vehicle_id = @external_vehicle_id
LIMIT 1;

-- name: EnsureReportedStop :one
WITH inserted AS (
    INSERT INTO reported_stop (agency_id, oba_stop_id)
    VALUES (@agency_id, @oba_stop_id)
    ON CONFLICT (agency_id, oba_stop_id) DO NOTHING
    RETURNING reported_stop_id
)
SELECT reported_stop_id FROM inserted
UNION ALL
SELECT reported_stop_id FROM reported_stop
WHERE agency_id = @agency_id AND oba_stop_id = @oba_stop_id
LIMIT 1;

-- name: EnsureReportedTrip :one
WITH inserted AS (
    INSERT INTO reported_trip (agency_id, oba_trip_id)
    VALUES (@agency_id, @oba_trip_id)
    ON CONFLICT (agency_id, oba_trip_id) DO NOTHING
    RETURNING reported_trip_id
)
SELECT reported_trip_id FROM inserted
UNION ALL
SELECT reported_trip_id FROM reported_trip
WHERE agency_id = @agency_id AND oba_trip_id = @oba_trip_id
LIMIT 1;
