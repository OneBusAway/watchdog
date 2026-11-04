package investigation

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5"
	"watchdog.onebusaway.org/internal/report"
)

func TestFail_Mapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		status   int
		title    string       // "" means no problem+json body
		reported sentry.Level // "" means no Sentry event
	}{
		{"no rows", fmt.Errorf("get episode: %w", pgx.ErrNoRows), http.StatusNotFound, "Not found", ""},
		{"timeout", fmt.Errorf("%w: canceling statement: secret-detail", errQueryTimeout), http.StatusServiceUnavailable, "Query timeout", sentry.LevelWarning},
		{"client canceled", fmt.Errorf("%w: context canceled", errRequestCanceled), statusClientClosedRequest, "", ""},
		{"anything else", errors.New("connection reset: secret-detail"), http.StatusInternalServerError, "Internal server error", sentry.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := report.CaptureSentry(t)
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/unmatched-stop-episodes/4812", nil)

			fail(rec, req, tc.err, logger)

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if strings.Contains(rec.Body.String(), "secret-detail") {
				t.Error("the error text was sent to the client")
			}
			if tc.title != "" {
				if body := decodeProblem(t, rec); body.Title != tc.title || body.Status != tc.status {
					t.Errorf("body = %+v, want title %q and status %d", body, tc.title, tc.status)
				}
			} else if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want none", rec.Body.String())
			}
			events := captured.Events()
			if tc.reported != "" {
				if len(events) != 1 {
					t.Fatalf("Sentry events = %d, want 1", len(events))
				}
				if got := events[0].Level; got != tc.reported {
					t.Errorf("Sentry level = %q, want %q", got, tc.reported)
				}
				if got := events[0].Tags["path"]; got != "/v1/unmatched-stop-episodes/4812" {
					t.Errorf("Sentry path tag = %q, want the request path", got)
				}
				if !strings.Contains(logs.String(), "secret-detail") {
					t.Error("the error was not logged")
				}
			} else if len(events) != 0 {
				t.Errorf("Sentry events = %d, want 0", len(events))
			}
		})
	}
}
