# sqlc queries

Each `.sql` file here holds named queries that `make sqlc` turns into typed Go
methods in `internal/db/dbgen`. sqlc checks every query against the schema
built from `internal/db/migrations`, so a query that no longer matches the
schema fails generation.

Queries are added together with the features that use them.
`GetOneBusAwayServerByURL` is the first one: it keeps the toolchain generating
and type-checking against the real schema, and the migration tests use it.
