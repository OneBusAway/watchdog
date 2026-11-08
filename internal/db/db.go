// Package db owns Watchdog's optional PostgreSQL investigation store: the
// connection pool, the embedded schema migrations, and the sqlc-generated
// query code in the dbgen subpackage.
//
// PostgreSQL is optional (see docs/adr/0001-durable-historical-investigation-storage.md).
// Watchdog keeps collecting and exporting Prometheus metrics without it, so
// callers treat an Open error as "investigation storage disabled", never as fatal.
//
// The schema is documented in docs/historical-investigation-schema.md.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migrations returns the embedded goose migration files, rooted at the
// migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		// fs.Sub only fails for an invalid path, and "migrations" is a constant.
		panic(err)
	}
	return sub
}

// Open connects to PostgreSQL, verifies the connection, and applies any
// pending schema migrations. On error no pool is returned and nothing is left
// open.
//
// databaseURL is a libpq-style URL or keyword/value string. It contains a
// password, so neither this function nor its callers should log it.
func Open(ctx context.Context, databaseURL string, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx redacts the password from parse errors.
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := Migrate(ctx, pool, logger); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

// Migrate applies every pending migration in Migrations().
//
// A PostgreSQL advisory lock is held while migrating, so if more than one
// Watchdog process ever starts at once, only one applies the migrations and
// the others wait, then find nothing left to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	provider, err := newProvider(pool)
	if err != nil {
		return err
	}
	// Closing the *sql.DB wrapper does not close the pool.
	defer provider.Close()

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}

	for _, result := range results {
		logger.Info("Applied database migration",
			"migration", result.Source.Path,
			"version", result.Source.Version,
			"duration", result.Duration)
	}
	return nil
}

// newProvider builds a goose provider over pool. It is shared by Migrate and
// the tests, which also need to migrate down.
func newProvider(pool *pgxpool.Pool) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		stdlib.OpenDBFromPool(pool),
		Migrations(),
		goose.WithSessionLocker(locker),
		// Only the SQL files in Migrations() are migrations; ignore any Go
		// migrations a dependency might register globally.
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return provider, nil
}
