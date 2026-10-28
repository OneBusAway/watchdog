-- Historical investigation schema.
--
-- Table meanings, rules, and the reasoning behind them are documented in
-- docs/historical-investigation-schema.md and ADR 0001. Keep this file and
-- that document in step: a change to one needs the matching change to the other.
--
-- SHA-256 columns hold lowercase hex (what Go's hex.EncodeToString produces).
-- They are fingerprints only, never foreign keys or retrieval references.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Group A: sources and identities
-- ---------------------------------------------------------------------------

CREATE TABLE onebusaway_server (
    server_id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    normalized_base_url TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agency (
    agency_id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id          BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    external_agency_id TEXT NOT NULL,
    current_name       TEXT,
    -- Agency IDs are only unique within one OBA server.
    UNIQUE (server_id, external_agency_id)
);

CREATE TABLE vehicle_position_endpoint (
    endpoint_id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id                       BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    normalized_vehicle_position_url TEXT NOT NULL,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Identity is the URL, not the position in the config file.
    UNIQUE (server_id, normalized_vehicle_position_url)
);

CREATE TABLE vehicle_entity (
    vehicle_entity_id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    endpoint_id         BIGINT NOT NULL REFERENCES vehicle_position_endpoint (endpoint_id),
    external_vehicle_id TEXT NOT NULL,
    -- Vehicle IDs are only unique within one feed.
    UNIQUE (endpoint_id, external_vehicle_id)
);

CREATE TABLE static_gtfs_source (
    source_id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id             BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    normalized_source_url TEXT NOT NULL,
    UNIQUE (server_id, normalized_source_url)
);

CREATE TABLE reported_stop (
    reported_stop_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agency_id        BIGINT NOT NULL REFERENCES agency (agency_id),
    oba_stop_id      TEXT NOT NULL,
    UNIQUE (agency_id, oba_stop_id),
    -- Target for agency-scoped composite foreign keys.
    UNIQUE (reported_stop_id, agency_id)
);

CREATE TABLE reported_trip (
    reported_trip_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agency_id        BIGINT NOT NULL REFERENCES agency (agency_id),
    oba_trip_id      TEXT NOT NULL,
    UNIQUE (agency_id, oba_trip_id),
    UNIQUE (reported_trip_id, agency_id)
);

-- ---------------------------------------------------------------------------
-- Group B: static GTFS context
-- ---------------------------------------------------------------------------

CREATE TABLE gtfs_static_version (
    gtfs_static_version_id  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id               BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    published_at            TIMESTAMPTZ NOT NULL,
    combined_content_sha256 CHAR(64) CHECK (combined_content_sha256 ~ '^[0-9a-f]{64}$'),
    parser_version          TEXT
);

CREATE TABLE gtfs_static_version_source (
    gtfs_static_version_id BIGINT NOT NULL REFERENCES gtfs_static_version (gtfs_static_version_id),
    source_id              BIGINT NOT NULL REFERENCES static_gtfs_source (source_id),
    source_content_sha256  CHAR(64) NOT NULL CHECK (source_content_sha256 ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (gtfs_static_version_id, source_id)
);

CREATE TABLE gtfs_static_version_agency (
    gtfs_static_version_id BIGINT NOT NULL REFERENCES gtfs_static_version (gtfs_static_version_id),
    agency_id              BIGINT NOT NULL REFERENCES agency (agency_id),
    PRIMARY KEY (gtfs_static_version_id, agency_id)
);

CREATE TABLE stop_metadata_version (
    stop_metadata_version_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    gtfs_static_version_id   BIGINT NOT NULL REFERENCES gtfs_static_version (gtfs_static_version_id),
    agency_id                BIGINT NOT NULL,
    reported_stop_id         BIGINT NOT NULL,
    static_stop_found        BOOLEAN NOT NULL,
    stop_name                TEXT,
    latitude                 DOUBLE PRECISION,
    longitude                DOUBLE PRECISION,
    parent_station_id        TEXT,
    location_type            SMALLINT,
    UNIQUE (gtfs_static_version_id, reported_stop_id),
    -- Target for the episode link table.
    UNIQUE (stop_metadata_version_id, reported_stop_id, agency_id),
    FOREIGN KEY (reported_stop_id, agency_id) REFERENCES reported_stop (reported_stop_id, agency_id),
    -- A "not found" row carries no static stop fields.
    CONSTRAINT stop_metadata_not_found_is_empty CHECK (
        static_stop_found OR (
            stop_name IS NULL AND latitude IS NULL AND longitude IS NULL
            AND parent_station_id IS NULL AND location_type IS NULL
        )
    ),
    CONSTRAINT stop_metadata_coordinates_paired CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CONSTRAINT stop_metadata_latitude_range CHECK (latitude BETWEEN -90 AND 90),
    CONSTRAINT stop_metadata_longitude_range CHECK (longitude BETWEEN -180 AND 180),
    CONSTRAINT stop_metadata_location_type_range CHECK (location_type BETWEEN 0 AND 5)
);

CREATE TABLE trip_metadata_version (
    trip_metadata_version_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    gtfs_static_version_id   BIGINT NOT NULL REFERENCES gtfs_static_version (gtfs_static_version_id),
    agency_id                BIGINT NOT NULL,
    reported_trip_id         BIGINT NOT NULL,
    static_trip_found        BOOLEAN NOT NULL,
    route_id                 TEXT,
    service_id               TEXT,
    block_id                 TEXT,
    UNIQUE (gtfs_static_version_id, reported_trip_id),
    UNIQUE (trip_metadata_version_id, reported_trip_id, agency_id),
    FOREIGN KEY (reported_trip_id, agency_id) REFERENCES reported_trip (reported_trip_id, agency_id),
    CONSTRAINT trip_metadata_not_found_is_empty CHECK (
        static_trip_found OR (route_id IS NULL AND service_id IS NULL AND block_id IS NULL)
    )
);

CREATE TABLE bounding_box_version (
    bounding_box_version_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    gtfs_static_version_id  BIGINT NOT NULL REFERENCES gtfs_static_version (gtfs_static_version_id),
    server_id               BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    -- Null means the server-wide union box.
    agency_id               BIGINT REFERENCES agency (agency_id),
    minimum_latitude        DOUBLE PRECISION NOT NULL CHECK (minimum_latitude BETWEEN -90 AND 90),
    maximum_latitude        DOUBLE PRECISION NOT NULL CHECK (maximum_latitude BETWEEN -90 AND 90),
    minimum_longitude       DOUBLE PRECISION NOT NULL CHECK (minimum_longitude BETWEEN -180 AND 180),
    maximum_longitude       DOUBLE PRECISION NOT NULL CHECK (maximum_longitude BETWEEN -180 AND 180),
    CONSTRAINT bounding_box_latitude_order CHECK (minimum_latitude <= maximum_latitude),
    CONSTRAINT bounding_box_longitude_order CHECK (minimum_longitude <= maximum_longitude)
);

-- One server-wide box per static version, and one box per agency per static version.
CREATE UNIQUE INDEX bounding_box_one_server_box_per_version
    ON bounding_box_version (gtfs_static_version_id)
    WHERE agency_id IS NULL;

CREATE UNIQUE INDEX bounding_box_one_agency_box_per_version
    ON bounding_box_version (gtfs_static_version_id, agency_id)
    WHERE agency_id IS NOT NULL;

CREATE TABLE cluster_scheme (
    scheme_version   TEXT PRIMARY KEY,
    s2_level         SMALLINT NOT NULL CHECK (s2_level BETWEEN 0 AND 30),
    station_grouping TEXT NOT NULL,
    description      TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Target that keeps s2_cell.cell_level consistent with its scheme.
    UNIQUE (scheme_version, s2_level)
);

CREATE TABLE s2_cell (
    scheme_version   TEXT NOT NULL,
    cell_id          TEXT NOT NULL,
    cell_level       SMALLINT NOT NULL,
    center_latitude  DOUBLE PRECISION NOT NULL CHECK (center_latitude BETWEEN -90 AND 90),
    center_longitude DOUBLE PRECISION NOT NULL CHECK (center_longitude BETWEEN -180 AND 180),
    PRIMARY KEY (scheme_version, cell_id),
    FOREIGN KEY (scheme_version, cell_level) REFERENCES cluster_scheme (scheme_version, s2_level)
);

CREATE TABLE stop_cluster_assignment (
    stop_metadata_version_id BIGINT NOT NULL REFERENCES stop_metadata_version (stop_metadata_version_id),
    scheme_version           TEXT NOT NULL,
    cell_id                  TEXT NOT NULL,
    -- The root of the stop's parent_station chain in Watchdog's static GTFS,
    -- stored as values: a station is usually not a reported stop, so it has
    -- no stop_metadata_version row to reference. Both null: no station.
    root_station_id          TEXT,
    root_station_name        TEXT,
    PRIMARY KEY (stop_metadata_version_id, scheme_version),
    FOREIGN KEY (scheme_version, cell_id) REFERENCES s2_cell (scheme_version, cell_id),
    CONSTRAINT stop_cluster_assignment_station_name_needs_id
        CHECK (root_station_name IS NULL OR root_station_id IS NOT NULL)
);

-- ---------------------------------------------------------------------------
-- Group C: OBA unmatched reports
-- ---------------------------------------------------------------------------

CREATE TABLE onebusaway_metrics_response (
    response_id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id             BIGINT NOT NULL REFERENCES onebusaway_server (server_id),
    observed_at           TIMESTAMPTZ NOT NULL,
    upstream_current_time TIMESTAMPTZ,
    api_version           INTEGER
);

CREATE TABLE onebusaway_metrics_response_agency (
    response_agency_id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    response_id          BIGINT NOT NULL REFERENCES onebusaway_metrics_response (response_id),
    agency_id            BIGINT NOT NULL REFERENCES agency (agency_id),
    -- TRUE only when the HTTP call and JSON decoding succeeded and the agency
    -- appeared in agencyIDs. Only such rows can open or close an episode.
    response_complete    BOOLEAN NOT NULL,
    unmatched_stop_count INTEGER CHECK (unmatched_stop_count >= 0),
    unmatched_trip_count INTEGER CHECK (unmatched_trip_count >= 0),
    matched_stop_count   INTEGER CHECK (matched_stop_count >= 0),
    matched_trip_count   INTEGER CHECK (matched_trip_count >= 0),
    UNIQUE (response_id, agency_id),
    -- Target for agency-scoped composite foreign keys from episodes.
    UNIQUE (response_agency_id, agency_id)
);

-- close_reason has one value today ('not_reported'). It is kept so later
-- reasons can be added by extending the CHECK list, without a column change.
CREATE TABLE unmatched_stop_episode (
    unmatched_stop_episode_id  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reported_stop_id           BIGINT NOT NULL,
    agency_id                  BIGINT NOT NULL,
    first_response_agency_id   BIGINT NOT NULL,
    closing_response_agency_id BIGINT,
    last_reported_at           TIMESTAMPTZ NOT NULL,
    close_reason               TEXT CHECK (close_reason IN ('not_reported')),
    UNIQUE (unmatched_stop_episode_id, reported_stop_id, agency_id),
    FOREIGN KEY (reported_stop_id, agency_id) REFERENCES reported_stop (reported_stop_id, agency_id),
    -- These stop one agency's response from opening or closing another agency's episode.
    FOREIGN KEY (first_response_agency_id, agency_id)
        REFERENCES onebusaway_metrics_response_agency (response_agency_id, agency_id),
    FOREIGN KEY (closing_response_agency_id, agency_id)
        REFERENCES onebusaway_metrics_response_agency (response_agency_id, agency_id),
    CONSTRAINT unmatched_stop_episode_closed_consistently
        CHECK ((close_reason IS NULL) = (closing_response_agency_id IS NULL))
);

CREATE TABLE unmatched_trip_episode (
    unmatched_trip_episode_id  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reported_trip_id           BIGINT NOT NULL,
    agency_id                  BIGINT NOT NULL,
    first_response_agency_id   BIGINT NOT NULL,
    closing_response_agency_id BIGINT,
    last_reported_at           TIMESTAMPTZ NOT NULL,
    close_reason               TEXT CHECK (close_reason IN ('not_reported')),
    UNIQUE (unmatched_trip_episode_id, reported_trip_id, agency_id),
    FOREIGN KEY (reported_trip_id, agency_id) REFERENCES reported_trip (reported_trip_id, agency_id),
    FOREIGN KEY (first_response_agency_id, agency_id)
        REFERENCES onebusaway_metrics_response_agency (response_agency_id, agency_id),
    FOREIGN KEY (closing_response_agency_id, agency_id)
        REFERENCES onebusaway_metrics_response_agency (response_agency_id, agency_id),
    CONSTRAINT unmatched_trip_episode_closed_consistently
        CHECK ((close_reason IS NULL) = (closing_response_agency_id IS NULL))
);

-- Only a complete response (response_complete = TRUE) may open or close an
-- unmatched episode. A CHECK cannot read another table, so a trigger enforces it.
-- +goose StatementBegin
CREATE FUNCTION require_complete_metrics_responses() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM onebusaway_metrics_response_agency
        WHERE response_agency_id = NEW.first_response_agency_id AND response_complete
    ) THEN
        RAISE EXCEPTION 'first response % is not a complete response', NEW.first_response_agency_id
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.closing_response_agency_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM onebusaway_metrics_response_agency
        WHERE response_agency_id = NEW.closing_response_agency_id AND response_complete
    ) THEN
        RAISE EXCEPTION 'closing response % is not a complete response', NEW.closing_response_agency_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER unmatched_stop_episode_complete_responses
    BEFORE INSERT OR UPDATE OF first_response_agency_id, closing_response_agency_id
    ON unmatched_stop_episode
    FOR EACH ROW EXECUTE FUNCTION require_complete_metrics_responses();

CREATE TRIGGER unmatched_trip_episode_complete_responses
    BEFORE INSERT OR UPDATE OF first_response_agency_id, closing_response_agency_id
    ON unmatched_trip_episode
    FOR EACH ROW EXECUTE FUNCTION require_complete_metrics_responses();

CREATE TABLE unmatched_stop_episode_metadata (
    unmatched_stop_episode_id BIGINT NOT NULL,
    stop_metadata_version_id  BIGINT NOT NULL,
    reported_stop_id          BIGINT NOT NULL,
    agency_id                 BIGINT NOT NULL,
    PRIMARY KEY (unmatched_stop_episode_id, stop_metadata_version_id),
    -- Together these guarantee both sides refer to the same stop and agency.
    FOREIGN KEY (unmatched_stop_episode_id, reported_stop_id, agency_id)
        REFERENCES unmatched_stop_episode (unmatched_stop_episode_id, reported_stop_id, agency_id),
    FOREIGN KEY (stop_metadata_version_id, reported_stop_id, agency_id)
        REFERENCES stop_metadata_version (stop_metadata_version_id, reported_stop_id, agency_id)
);

CREATE TABLE unmatched_trip_episode_metadata (
    unmatched_trip_episode_id BIGINT NOT NULL,
    trip_metadata_version_id  BIGINT NOT NULL,
    reported_trip_id          BIGINT NOT NULL,
    agency_id                 BIGINT NOT NULL,
    PRIMARY KEY (unmatched_trip_episode_id, trip_metadata_version_id),
    FOREIGN KEY (unmatched_trip_episode_id, reported_trip_id, agency_id)
        REFERENCES unmatched_trip_episode (unmatched_trip_episode_id, reported_trip_id, agency_id),
    FOREIGN KEY (trip_metadata_version_id, reported_trip_id, agency_id)
        REFERENCES trip_metadata_version (trip_metadata_version_id, reported_trip_id, agency_id)
);

-- ---------------------------------------------------------------------------
-- Group D: vehicle anomalies
-- ---------------------------------------------------------------------------

CREATE TABLE vehicle_position_response (
    vehicle_position_response_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    endpoint_id                  BIGINT NOT NULL REFERENCES vehicle_position_endpoint (endpoint_id),
    observed_at                  TIMESTAMPTZ NOT NULL,
    source_header_time           TIMESTAMPTZ,
    incrementality               TEXT NOT NULL CHECK (incrementality IN ('FULL_DATASET', 'DIFFERENTIAL')),
    payload_sha256               CHAR(64) CHECK (payload_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE vehicle_position_evidence (
    vehicle_position_evidence_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    vehicle_position_response_id BIGINT NOT NULL REFERENCES vehicle_position_response (vehicle_position_response_id),
    -- Null when the feed entity has no VehicleDescriptor.id.
    vehicle_entity_id            BIGINT REFERENCES vehicle_entity (vehicle_entity_id),
    feed_entity_id               TEXT,
    agency_id                    BIGINT REFERENCES agency (agency_id),
    attribution_reason           TEXT,
    vehicle_source_time          TIMESTAMPTZ,
    -- Raw values with no range check: an invalid value is what is being investigated.
    latitude                     DOUBLE PRECISION,
    longitude                    DOUBLE PRECISION,
    position_valid               BOOLEAN NOT NULL,
    position_validation_reason   TEXT,
    reported_speed_mps           DOUBLE PRECISION,
    computed_speed_mps           DOUBLE PRECISION,
    speed_discrepancy_ratio      DOUBLE PRECISION,
    current_status               SMALLINT,
    trip_id                      TEXT,
    route_id                     TEXT,
    trip_start_date              DATE,
    -- Seconds after local service-day midnight; may exceed 86,400.
    trip_start_time_seconds      INTEGER,
    schedule_relationship        TEXT,
    expected_schedule_state      TEXT CHECK (expected_schedule_state IN ('expected_active', 'expected_inactive', 'unknown')),
    expected_schedule_reason     TEXT,
    expected_window_start        TIMESTAMPTZ,
    expected_window_end          TIMESTAMPTZ,
    gtfs_static_version_id       BIGINT REFERENCES gtfs_static_version (gtfs_static_version_id),
    bounding_box_version_id      BIGINT REFERENCES bounding_box_version (bounding_box_version_id),
    -- The reason is present exactly when the position failed validation. Codes
    -- mirror geo.IsValidLatLon and vehicleLatLon in internal/metrics.
    CONSTRAINT vehicle_position_evidence_validation_reason CHECK (
        (position_valid AND position_validation_reason IS NULL)
        OR (NOT position_valid AND position_validation_reason IS NOT NULL AND position_validation_reason IN (
            'missing_position',
            'non_finite_coordinate',
            'placeholder_zero_coordinate',
            'latitude_out_of_range',
            'longitude_out_of_range'
        ))
    ),
    -- Codes are the gtfs.AttributionFailureReason values.
    CONSTRAINT vehicle_position_evidence_attribution_reason CHECK (attribution_reason IN (
        'missing_vehicle_id',
        'missing_route_id',
        'unknown_route_id',
        'agency_not_reported_by_oba',
        'ambiguous_identifier',
        'route_trip_conflict'
    )),
    -- Each schedule state has its own set of reasons; both are null when no
    -- schedule classification was made. COALESCE turns an unknown result into
    -- FALSE: comparisons with a NULL state or reason are unknown, and a CHECK
    -- that evaluates to unknown would otherwise pass.
    CONSTRAINT vehicle_position_evidence_schedule_reason CHECK (COALESCE(
        (expected_schedule_state IS NULL AND expected_schedule_reason IS NULL)
        OR (expected_schedule_state = 'unknown' AND expected_schedule_reason IS NOT NULL AND expected_schedule_reason IN (
            'no_trip_descriptor',
            'trip_not_in_static_gtfs',
            'trip_instance_ambiguous'
        ))
        OR (expected_schedule_state = 'expected_inactive' AND expected_schedule_reason IS NOT NULL AND expected_schedule_reason IN (
            'service_not_active_on_date',
            'before_scheduled_window',
            'after_scheduled_window'
        ))
        OR (expected_schedule_state = 'expected_active' AND expected_schedule_reason = 'within_scheduled_window'),
        FALSE
    ))
);

-- One evidence row per identified vehicle per feed response.
CREATE UNIQUE INDEX vehicle_position_evidence_one_per_vehicle_response
    ON vehicle_position_evidence (vehicle_position_response_id, vehicle_entity_id)
    WHERE vehicle_entity_id IS NOT NULL;

CREATE TABLE vehicle_anomaly_type (
    anomaly_type_id SMALLINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    anomaly_code    TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL
);

-- reported_speed_plausibility is added only after its threshold is approved.
INSERT INTO vehicle_anomaly_type (anomaly_code, description) VALUES
    ('stale_source_timestamp', 'The vehicle''s GTFS-Realtime source timestamp is older than the stale threshold.'),
    ('invalid_coordinate', 'The vehicle position has coordinates that fail validation.'),
    ('stopped_out_of_bounds', 'The vehicle is STOPPED_AT a location outside the applicable bounding box.');

CREATE TABLE vehicle_anomaly_rule_version (
    rule_version_id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    anomaly_type_id              SMALLINT NOT NULL REFERENCES vehicle_anomaly_type (anomaly_type_id),
    -- Per-type constant in Watchdog code, bumped when the detection logic changes.
    rule_logic_version           INTEGER NOT NULL CHECK (rule_logic_version >= 1),
    version_name                 TEXT NOT NULL,
    watchdog_build_version       TEXT,
    -- When Watchdog started applying this rule. Not an episode's start time.
    effective_at                 TIMESTAMPTZ NOT NULL,
    stale_age_threshold_seconds  INTEGER CHECK (stale_age_threshold_seconds >= 0),
    reported_speed_threshold_mps DOUBLE PRECISION CHECK (reported_speed_threshold_mps >= 0),
    -- version_name is not unique: a rule can return to earlier values later.
    UNIQUE (anomaly_type_id, effective_at)
);

CREATE TABLE vehicle_anomaly_episode (
    vehicle_anomaly_episode_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    vehicle_entity_id          BIGINT NOT NULL REFERENCES vehicle_entity (vehicle_entity_id),
    anomaly_type_id            SMALLINT NOT NULL REFERENCES vehicle_anomaly_type (anomaly_type_id),
    rule_version_id            BIGINT NOT NULL REFERENCES vehicle_anomaly_rule_version (rule_version_id),
    opening_response_id        BIGINT NOT NULL REFERENCES vehicle_position_response (vehicle_position_response_id),
    closing_response_id        BIGINT REFERENCES vehicle_position_response (vehicle_position_response_id),
    -- Close reasons describe the observed closing condition, not a physical cause.
    close_reason               TEXT CHECK (close_reason IN (
        'fresh_source_timestamp',
        'not_present_in_full_dataset',
        'coordinates_valid_again',
        'inside_bounding_box',
        'no_longer_stopped_at_stop'
    )),
    CONSTRAINT vehicle_anomaly_episode_closed_consistently
        CHECK ((close_reason IS NULL) = (closing_response_id IS NULL))
);

-- The opening and closing responses must come from the vehicle's own endpoint,
-- and the closing response must not be observed before the opening one.
-- Plain foreign keys cannot compare rows, so a trigger enforces both.
-- +goose StatementBegin
CREATE FUNCTION check_vehicle_anomaly_episode_responses() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    vehicle_endpoint BIGINT;
    opening_endpoint BIGINT;
    opening_observed TIMESTAMPTZ;
    closing_endpoint BIGINT;
    closing_observed TIMESTAMPTZ;
BEGIN
    SELECT endpoint_id INTO vehicle_endpoint
        FROM vehicle_entity WHERE vehicle_entity_id = NEW.vehicle_entity_id;
    SELECT endpoint_id, observed_at INTO opening_endpoint, opening_observed
        FROM vehicle_position_response WHERE vehicle_position_response_id = NEW.opening_response_id;

    IF opening_endpoint IS DISTINCT FROM vehicle_endpoint THEN
        RAISE EXCEPTION 'opening response % is not from the vehicle''s endpoint', NEW.opening_response_id
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.closing_response_id IS NOT NULL THEN
        SELECT endpoint_id, observed_at INTO closing_endpoint, closing_observed
            FROM vehicle_position_response WHERE vehicle_position_response_id = NEW.closing_response_id;
        IF closing_endpoint IS DISTINCT FROM vehicle_endpoint THEN
            RAISE EXCEPTION 'closing response % is not from the vehicle''s endpoint', NEW.closing_response_id
                USING ERRCODE = 'check_violation';
        END IF;
        IF closing_observed < opening_observed THEN
            RAISE EXCEPTION 'closing response % was observed before the opening response', NEW.closing_response_id
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER vehicle_anomaly_episode_responses
    BEFORE INSERT OR UPDATE OF vehicle_entity_id, opening_response_id, closing_response_id
    ON vehicle_anomaly_episode
    FOR EACH ROW EXECUTE FUNCTION check_vehicle_anomaly_episode_responses();

CREATE TABLE vehicle_anomaly_episode_evidence (
    vehicle_anomaly_episode_id   BIGINT NOT NULL REFERENCES vehicle_anomaly_episode (vehicle_anomaly_episode_id),
    vehicle_position_evidence_id BIGINT NOT NULL REFERENCES vehicle_position_evidence (vehicle_position_evidence_id),
    evidence_role                TEXT NOT NULL CHECK (evidence_role IN ('opening', 'meaningful_change', 'resolution')),
    PRIMARY KEY (vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role)
);

-- One opening and one resolution link per episode; any number of meaningful changes.
CREATE UNIQUE INDEX vehicle_anomaly_episode_one_opening
    ON vehicle_anomaly_episode_evidence (vehicle_anomaly_episode_id)
    WHERE evidence_role = 'opening';

CREATE UNIQUE INDEX vehicle_anomaly_episode_one_resolution
    ON vehicle_anomaly_episode_evidence (vehicle_anomaly_episode_id)
    WHERE evidence_role = 'resolution';

-- ---------------------------------------------------------------------------
-- Access and one-open-episode indexes
-- ---------------------------------------------------------------------------

CREATE INDEX onebusaway_metrics_response_server_time
    ON onebusaway_metrics_response (server_id, observed_at DESC);

CREATE INDEX onebusaway_metrics_response_agency_agency_time
    ON onebusaway_metrics_response_agency (agency_id, response_id);

CREATE INDEX vehicle_position_response_endpoint_time
    ON vehicle_position_response (endpoint_id, observed_at DESC);

CREATE INDEX vehicle_position_evidence_vehicle_time
    ON vehicle_position_evidence (vehicle_entity_id, vehicle_position_response_id)
    WHERE vehicle_entity_id IS NOT NULL;

CREATE INDEX unmatched_stop_episode_entity
    ON unmatched_stop_episode (reported_stop_id, first_response_agency_id);

CREATE INDEX unmatched_trip_episode_entity
    ON unmatched_trip_episode (reported_trip_id, first_response_agency_id);

CREATE INDEX vehicle_anomaly_episode_entity_type
    ON vehicle_anomaly_episode (vehicle_entity_id, anomaly_type_id, closing_response_id);

CREATE INDEX vehicle_anomaly_episode_opening
    ON vehicle_anomaly_episode (opening_response_id);

CREATE UNIQUE INDEX unmatched_stop_one_open_per_entity
    ON unmatched_stop_episode (reported_stop_id)
    WHERE closing_response_agency_id IS NULL;

CREATE UNIQUE INDEX unmatched_trip_one_open_per_entity
    ON unmatched_trip_episode (reported_trip_id)
    WHERE closing_response_agency_id IS NULL;

CREATE UNIQUE INDEX vehicle_anomaly_one_open_per_type
    ON vehicle_anomaly_episode (vehicle_entity_id, anomaly_type_id)
    WHERE closing_response_id IS NULL;

-- Foreign-key indexes. PostgreSQL does not create these automatically; they
-- serve episode/evidence joins and keep retention deletes from scanning.
CREATE INDEX unmatched_stop_episode_closing ON unmatched_stop_episode (closing_response_agency_id);
CREATE INDEX unmatched_trip_episode_closing ON unmatched_trip_episode (closing_response_agency_id);
CREATE INDEX unmatched_stop_episode_metadata_version ON unmatched_stop_episode_metadata (stop_metadata_version_id);
CREATE INDEX unmatched_trip_episode_metadata_version ON unmatched_trip_episode_metadata (trip_metadata_version_id);
CREATE INDEX stop_metadata_version_reported_stop ON stop_metadata_version (reported_stop_id, gtfs_static_version_id);
CREATE INDEX trip_metadata_version_reported_trip ON trip_metadata_version (reported_trip_id, gtfs_static_version_id);
CREATE INDEX stop_cluster_assignment_cell ON stop_cluster_assignment (scheme_version, cell_id);
CREATE INDEX vehicle_position_evidence_static_version ON vehicle_position_evidence (gtfs_static_version_id);
CREATE INDEX vehicle_position_evidence_bounding_box ON vehicle_position_evidence (bounding_box_version_id);
CREATE INDEX vehicle_anomaly_episode_closing ON vehicle_anomaly_episode (closing_response_id);
CREATE INDEX vehicle_anomaly_episode_evidence_evidence ON vehicle_anomaly_episode_evidence (vehicle_position_evidence_id);

-- +goose Down

DROP TABLE vehicle_anomaly_episode_evidence;
DROP TABLE vehicle_anomaly_episode;
DROP FUNCTION check_vehicle_anomaly_episode_responses();
DROP TABLE vehicle_anomaly_rule_version;
DROP TABLE vehicle_anomaly_type;
DROP TABLE vehicle_position_evidence;
DROP TABLE vehicle_position_response;
DROP TABLE unmatched_trip_episode_metadata;
DROP TABLE unmatched_stop_episode_metadata;
DROP TABLE unmatched_trip_episode;
DROP TABLE unmatched_stop_episode;
DROP FUNCTION require_complete_metrics_responses();
DROP TABLE onebusaway_metrics_response_agency;
DROP TABLE onebusaway_metrics_response;
DROP TABLE stop_cluster_assignment;
DROP TABLE s2_cell;
DROP TABLE cluster_scheme;
DROP TABLE bounding_box_version;
DROP TABLE trip_metadata_version;
DROP TABLE stop_metadata_version;
DROP TABLE gtfs_static_version_agency;
DROP TABLE gtfs_static_version_source;
DROP TABLE gtfs_static_version;
DROP TABLE reported_trip;
DROP TABLE reported_stop;
DROP TABLE static_gtfs_source;
DROP TABLE vehicle_entity;
DROP TABLE vehicle_position_endpoint;
DROP TABLE agency;
DROP TABLE onebusaway_server;
