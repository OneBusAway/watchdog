package history

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// testDatabaseURLEnv names the PostgreSQL database the tests run against.
// Tests that need it skip when it is unset, so `go test ./...` works without
// PostgreSQL; CI sets it.
const testDatabaseURLEnv = "WATCHDOG_TEST_DATABASE_URL"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// newMigratedPool returns a pool on a fresh schema with the investigation
// migrations applied. The schema is dropped when the test ends.
func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Skipf("%s is not set; skipping PostgreSQL test", testDatabaseURLEnv)
	}

	ctx := context.Background()
	schema := fmt.Sprintf("watchdog_history_test_%d", rand.Uint64())

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
		t.Fatalf("parse %s: %v", testDatabaseURLEnv, err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to test schema: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.Migrate(ctx, pool, discardLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// inTx runs fn in a transaction that the test commits, failing the test on
// any error.
func inTx(t *testing.T, pool *pgxpool.Pool, fn func(*dbgen.Queries) error) {
	t.Helper()
	if err := runInTx(context.Background(), pool, fn); err != nil {
		t.Fatalf("transaction: %v", err)
	}
}

// count returns the number of rows in a table.
func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
