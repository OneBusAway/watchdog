// Package dbtest gives tests their own PostgreSQL schema to work in. Every
// package with database tests (internal/db, internal/investigation, and later
// internal/history) uses it, so they all set up and clean up the same way.
//
// Tests that call it skip unless WATCHDOG_TEST_DATABASE_URL is set, so
// `go test ./...` works without PostgreSQL. CI sets it.
package dbtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db"
)

// DatabaseURLEnv names the PostgreSQL database the tests run against.
const DatabaseURLEnv = "WATCHDOG_TEST_DATABASE_URL"

// NewPool returns a pool whose connections use a fresh, empty schema, so each
// test starts from nothing without touching other tests' data. The schema is
// dropped when the test ends. The test is skipped when DatabaseURLEnv is
// unset.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv(DatabaseURLEnv)
	if databaseURL == "" {
		t.Skipf("%s is not set; skipping PostgreSQL test", DatabaseURLEnv)
	}

	ctx := context.Background()
	schema := fmt.Sprintf("watchdog_test_%d", rand.Uint64())

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse %s: %v", DatabaseURLEnv, err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to test schema: %v", err)
	}
	// Registered after the schema cleanup, so it runs first: connections are
	// closed before the schema is dropped.
	t.Cleanup(pool.Close)
	return pool
}

// NewMigratedPool returns a NewPool schema with every migration applied.
func NewMigratedPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := NewPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), pool, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool
}

// ID runs an INSERT ... RETURNING (or any query returning one integer) and
// returns the value. The test fails if the query does, so setup stays short.
func ID(t testing.TB, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return id
}

// Exec runs a statement and returns its error, so a test can check that a
// constraint rejects it.
func Exec(pool *pgxpool.Pool, query string, args ...any) error {
	_, err := pool.Exec(context.Background(), query, args...)
	return err
}
