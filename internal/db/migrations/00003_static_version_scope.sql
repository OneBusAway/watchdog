-- Which configured entry recorded a static version.
--
-- A server-scoped entry and an agency-scoped entry can share one base URL
-- and therefore one onebusaway_server row. Server-scoped reuse has to follow
-- that entry's own publications. An agency entry's later version would
-- otherwise look newer, and a restart would insert another copy of the
-- unchanged server feed. The default is true, so a row written before this
-- column existed, and any insert that does not set it, stays server-scoped.
-- The recorder sets the column explicitly.

-- +goose Up
ALTER TABLE gtfs_static_version
    ADD COLUMN server_scoped BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE gtfs_static_version
    DROP COLUMN server_scoped;
