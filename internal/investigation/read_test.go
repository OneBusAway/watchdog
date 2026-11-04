package investigation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/db/dbtest"
	"watchdog.onebusaway.org/internal/models"
)

// codeReadOnlyTransaction is PostgreSQL's read_only_sql_transaction error.
const codeReadOnlyTransaction = "25006"

func TestRead_ReadOnly(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)

	err := readTx(context.Background(), pool, queryTimeout, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n')")
		return err
	})

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != codeReadOnlyTransaction {
		t.Fatalf("write inside read: err = %v, want PostgreSQL error %s", err, codeReadOnlyTransaction)
	}
}

func TestRead_RepeatableRead(t *testing.T) {
	pool := dbtest.NewPool(t)

	var level string
	err := readTx(context.Background(), pool, queryTimeout, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level)
	})

	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if level != "repeatable read" {
		t.Errorf("isolation = %q, want %q", level, "repeatable read")
	}
}

// A row committed by Watchdog while a request is reading must not appear
// halfway through that request.
func TestRead_OneSnapshot(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	count := func(ctx context.Context, tx pgx.Tx) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM onebusaway_server").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	var before, after int
	err := readTx(context.Background(), pool, queryTimeout, func(ctx context.Context, tx pgx.Tx) error {
		before = count(ctx, tx)
		if err := dbtest.Exec(pool,
			"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n')"); err != nil {
			t.Fatalf("concurrent insert: %v", err)
		}
		after = count(ctx, tx)
		return nil
	})

	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if before != 0 || after != 0 {
		t.Errorf("counts inside one read = %d then %d, want 0 then 0", before, after)
	}
}

func TestRead_Timeout(t *testing.T) {
	pool := dbtest.NewPool(t)

	start := time.Now()
	err := readTx(context.Background(), pool, 100*time.Millisecond, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
		return err
	})

	if !errors.Is(err, errQueryTimeout) {
		t.Fatalf("err = %v, want errQueryTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("read took %v; the timeout did not stop the query", elapsed)
	}
}

func TestRead_ClientCanceled(t *testing.T) {
	pool := dbtest.NewPool(t)
	request, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	err := readTx(request, pool, queryTimeout, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
		return err
	})

	if !errors.Is(err, errRequestCanceled) {
		t.Fatalf("err = %v, want errRequestCanceled", err)
	}
	if errors.Is(err, errQueryTimeout) {
		t.Error("a client cancel was reported as a query timeout")
	}
}

func TestRead_ReturnsHandlerError(t *testing.T) {
	pool := dbtest.NewPool(t)

	err := readTx(context.Background(), pool, queryTimeout, func(context.Context, pgx.Tx) error {
		return pgx.ErrNoRows
	})

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
	if errors.Is(err, errQueryTimeout) || errors.Is(err, errRequestCanceled) {
		t.Errorf("err = %v was marked as a timeout or cancel", err)
	}
}

// read hands endpoints the generated queries, bound to the transaction.
func TestRead_GeneratedQueries(t *testing.T) {
	pool := dbtest.NewMigratedPool(t)
	if err := dbtest.Exec(pool,
		"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('https://api.example.com', 'Example')"); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewInvestigationService(pool, func() []models.ObaServer { return nil }, nil, logger)

	var name string
	err := s.read(context.Background(), func(ctx context.Context, q *dbgen.Queries) error {
		server, err := q.GetOneBusAwayServerByURL(ctx, "https://api.example.com")
		name = server.DisplayName
		return err
	})

	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "Example" {
		t.Errorf("display name = %q, want Example", name)
	}
}
