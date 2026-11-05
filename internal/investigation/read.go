package investigation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// queryTimeout bounds all the reads of one request.
const queryTimeout = 5 * time.Second

// errQueryTimeout marks a read that ran past queryTimeout. fail turns it into
// 503 "Query timeout".
var errQueryTimeout = errors.New("investigation query timed out")

// errRequestCanceled marks a read stopped because the client went away. fail
// does not report it to Sentry: nothing is wrong on our side.
var errRequestCanceled = errors.New("investigation request canceled")

// errDatabaseUnavailable marks a read that failed because PostgreSQL could not
// be reached or was shutting down or starting up. That is an outage, not a bug
// in a query, so fail answers 503 and reports a warning rather than a 500.
var errDatabaseUnavailable = errors.New("investigation database unavailable")

// read runs fn inside one REPEATABLE READ READ ONLY transaction, under the
// 5-second query timeout. Every query fn runs through q sees the same
// snapshot, so the parts of one response always agree, and the transaction
// rejects any write.
//
// fn must pass its ctx argument, not the request's context, to every query:
// that ctx carries the timeout.
func (s *InvestigationService) read(ctx context.Context, fn func(ctx context.Context, q *dbgen.Queries) error) error {
	return readTx(ctx, s.Pool, queryTimeout, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, dbgen.New(tx))
	})
}

// readTx is read with its dependencies explicit, and with the raw
// transaction, so tests can try a write and use a short timeout.
//
// An error is wrapped with errQueryTimeout when the timeout expired, or with
// errRequestCanceled when the caller's context ended first. PostgreSQL and
// pgx report both in several ways, so the context, not the error, decides.
// Otherwise, an error that shows the database could not be reached is wrapped
// with errDatabaseUnavailable.
func readTx(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration, fn func(ctx context.Context, tx pgx.Tx) error) error {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := runReadOnly(readCtx, pool, fn)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %w", errRequestCanceled, err)
	case errors.Is(readCtx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", errQueryTimeout, err)
	case databaseUnreachable(err):
		return fmt.Errorf("%w: %w", errDatabaseUnavailable, err)
	default:
		return err
	}
}

func runReadOnly(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	// Rollback after a successful Commit is a no-op.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit read-only transaction: %w", err)
	}
	return nil
}

// databaseUnreachable reports whether err shows that PostgreSQL could not be
// reached: no connection could be made, the connection broke, or the server
// was shutting down or starting up. Errors from a query itself (a syntax
// error, a missing column) are not included: those are bugs.
func databaseUnreachable(err error) bool {
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"): // connection exception
			return true
		case pgErr.Code == "57P01", // admin_shutdown
			pgErr.Code == "57P02", // crash_shutdown
			pgErr.Code == "57P03": // cannot_connect_now (starting up)
			return true
		}
	}
	return false
}
