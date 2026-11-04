package investigation

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"
	"watchdog.onebusaway.org/internal/report"
)

// statusClientClosedRequest is recorded when the client went away before the
// response was ready. 499 is not an HTTP standard code; it is nginx's
// convention, and it keeps client disconnects apart from server errors in the
// request metrics.
const statusClientClosedRequest = 499

// fail turns an error from read, or from building a response, into the
// response every endpoint uses.
func (s *InvestigationService) fail(w http.ResponseWriter, r *http.Request, err error) {
	fail(w, r, err, s.Logger)
}

// fail is InvestigationService.fail with the logger explicit.
//
//   - No rows: 404, because the URI names no resource (for example an episode
//     removed by retention).
//   - Query timeout: 503 "Query timeout", logged and reported to Sentry as a
//     warning: a query that slow usually means a missing index or an
//     unbounded filter, which is our bug.
//   - Client went away: 499, with no body and no Sentry report.
//   - Anything else: 500, logged and reported to Sentry. The error text is
//     never sent to the client.
func fail(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeProblem(w, http.StatusNotFound, "Not found", "No resource exists at this URI.", nil)
	case errors.Is(err, errQueryTimeout):
		logger.Warn("investigation query timed out", "method", r.Method, "path", r.URL.Path, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  sentryTags(r),
			Level: sentry.LevelWarning,
		})
		writeProblem(w, http.StatusServiceUnavailable, "Query timeout",
			"The request's queries took longer than 5 seconds.", nil)
	case errors.Is(err, errRequestCanceled):
		logger.Debug("investigation request canceled by client", "method", r.Method, "path", r.URL.Path)
		w.WriteHeader(statusClientClosedRequest)
	default:
		logger.Error("investigation request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:  sentryTags(r),
			Level: sentry.LevelError,
		})
		writeProblem(w, http.StatusInternalServerError, "Internal server error",
			"An unexpected error occurred.", nil)
	}
}

// sentryTags are the tags on every Investigation API report.
func sentryTags(r *http.Request) map[string]string {
	return map[string]string{"component": "investigation_api", "path": r.URL.Path}
}
