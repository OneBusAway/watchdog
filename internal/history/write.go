package history

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// writeTimeout bounds one history write, so a slow database cannot hold up
// collection for long.
const writeTimeout = 5 * time.Second

// writeTags identifies the server, and the agency when the step runs per
// agency, that a write belongs to. It feeds the log line, the Sentry tags and
// the failure counter's labels.
type writeTags struct {
	serverName string
	// serverURL is the sanitized base URL; it is only used as a metric
	// label, never sent to Sentry.
	serverURL  string
	agencyID   string
	agencyName string
}

// serverTags returns the tags for a step that runs once per server.
func serverTags(server models.ObaServer) writeTags {
	return writeTags{
		serverName: server.ServerName,
		serverURL:  utils.SanitizeServerURL(server.ObaBaseURL),
	}
}

// agencyTags returns the tags for a step that runs once per agency.
func agencyTags(server models.ObaServer, agencyID, agencyName string) writeTags {
	tags := serverTags(server)
	tags.agencyID = agencyID
	tags.agencyName = agencyName
	return tags
}

// sentryTags follows the collector's convention: server_name always, plus
// agency_id and agency_name when the step runs per agency.
func (t writeTags) sentryTags() map[string]string {
	tags := map[string]string{"server_name": t.serverName}
	if t.agencyID != "" {
		tags["agency_id"] = t.agencyID
		tags["agency_name"] = t.agencyName
	}
	return tags
}

// txBeginner is the part of *pgxpool.Pool that write needs. Tests substitute
// a failing implementation to exercise the error path without PostgreSQL.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// write runs fn in one read-write transaction. It never returns an error to
// collection: a failure is logged, reported to Sentry and counted, and the
// observation is lost. Nothing is buffered or retried.
func (h *HistoryService) write(ctx context.Context, step string, tags writeTags, fn func(*dbgen.Queries) error) {
	if h == nil || h.pool == nil {
		return
	}
	write(ctx, h.pool, h.logger, step, tags, fn)
}

func write(ctx context.Context, db txBeginner, logger *slog.Logger, step string, tags writeTags, fn func(*dbgen.Queries) error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	if err := runInTx(ctx, db, fn); err != nil {
		reportWriteFailure(logger, step, tags, err)
	}
}

func runInTx(ctx context.Context, db txBeginner, fn func(*dbgen.Queries) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Rollback after a successful Commit is a no-op.
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := fn(dbgen.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func reportWriteFailure(logger *slog.Logger, step string, tags writeTags, err error) {
	logger.Error("Failed to write investigation history",
		"step", step,
		"server_name", tags.serverName,
		"agency_id", tags.agencyID,
		"agency_name", tags.agencyName,
		"error", err)
	report.ReportErrorWithSentryOptions(fmt.Errorf("write investigation history (%s): %w", step, err), report.SentryReportOptions{
		Tags:  tags.sentryTags(),
		Level: sentry.LevelError,
	})
	metrics.InvestigationHistoryWriteFailures.WithLabelValues(step, tags.serverName, tags.serverURL).Inc()
}
