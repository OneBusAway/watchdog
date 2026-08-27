//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// errorRecorder is a slog.Handler that records error-level messages.
//
// The GTFS download path reports failures through its injected logger and
// Sentry rather than returning them, so a test that only inspects the stores
// cannot tell a total failure from a partial one -- or, for a multi-feed
// server, from a merge that quietly proceeded on the one feed that worked.
// Recording error records turns that documented blind spot into an assertion.
type errorRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func newErrorRecorder() *errorRecorder {
	return &errorRecorder{}
}

func (r *errorRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (r *errorRecorder) Handle(_ context.Context, record slog.Record) error {
	var b strings.Builder
	b.WriteString(record.Message)
	record.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, b.String())
	return nil
}

// WithAttrs and WithGroup return the receiver: the recorder only needs the
// message and its inline attributes, and downloads run concurrently, so there
// is no per-handler state worth cloning.
func (r *errorRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return r }
func (r *errorRecorder) WithGroup(_ string) slog.Handler      { return r }

// Errors returns a copy of the recorded error messages.
func (r *errorRecorder) Errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}
