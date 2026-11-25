-- Which configured entry recorded a static version.
--
-- A server-scoped entry and an agency-scoped entry can share one base URL
-- and therefore one onebusaway_server row. Server-scoped reuse has to follow
-- that entry's own publications. An agency entry's later version would
-- otherwise look newer, and a restart would insert another copy of the
-- unchanged server feed. Rows written before this column existed are marked
-- server-scoped: the store could not tell the two apart, and a rollback
-- already recorded has to stay visible to server-scoped reuse.

-- +goose Up
ALTER TABLE gtfs_static_version
    ADD COLUMN server_scoped BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE gtfs_static_version
    ALTER COLUMN server_scoped DROP DEFAULT;

-- +goose Down
ALTER TABLE gtfs_static_version
    DROP COLUMN server_scoped;
