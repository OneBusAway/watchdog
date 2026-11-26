package report

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/getsentry/sentry-go"
)

// onlyEvent returns the single captured event, failing the test otherwise.
func onlyEvent(t *testing.T, rec *RecordingTransport) *sentry.Event {
	t.Helper()
	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("captured %d Sentry events, want 1", len(events))
	}
	return events[0]
}

// exceptionValue returns the message of the event's last exception, which is
// the error passed to CaptureException.
func exceptionValue(t *testing.T, event *sentry.Event) string {
	t.Helper()
	if len(event.Exception) == 0 {
		t.Fatal("event has no exception")
	}
	return event.Exception[len(event.Exception)-1].Value
}

func TestReportErrorIgnoresNilError(t *testing.T) {
	rec := CaptureSentry(t)

	ReportError(nil)
	ReportError(nil, sentry.LevelWarning)

	if got := len(rec.Events()); got != 0 {
		t.Fatalf("captured %d Sentry events for a nil error, want 0", got)
	}
}

func TestReportErrorLevel(t *testing.T) {
	tests := []struct {
		name   string
		levels []sentry.Level
		want   sentry.Level
	}{
		{name: "defaults to error", want: sentry.LevelError},
		{name: "uses given level", levels: []sentry.Level{sentry.LevelWarning}, want: sentry.LevelWarning},
		{name: "uses first of several levels", levels: []sentry.Level{sentry.LevelInfo, sentry.LevelFatal}, want: sentry.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := CaptureSentry(t)

			ReportError(errors.New("feed fetch failed"), tt.levels...)

			event := onlyEvent(t, rec)
			if event.Level != tt.want {
				t.Errorf("level = %q, want %q", event.Level, tt.want)
			}
			if got := exceptionValue(t, event); got != "feed fetch failed" {
				t.Errorf("exception value = %q, want %q", got, "feed fetch failed")
			}
		})
	}
}

func TestReportErrorWithSentryOptionsIgnoresNilError(t *testing.T) {
	rec := CaptureSentry(t)

	ReportErrorWithSentryOptions(nil, SentryReportOptions{
		Tags: map[string]string{"server_name": "test-server"},
	})

	if got := len(rec.Events()); got != 0 {
		t.Fatalf("captured %d Sentry events for a nil error, want 0", got)
	}
}

func TestReportErrorWithSentryOptionsSetsTagsContextAndLevel(t *testing.T) {
	rec := CaptureSentry(t)

	ReportErrorWithSentryOptions(errors.New("metrics fetch failed"), SentryReportOptions{
		Tags: map[string]string{
			"agency_id":   "40",
			"agency_name": "Sound Transit",
			"server_name": "test-server",
		},
		ExtraContext: map[string]interface{}{
			"url":         "https://oba.example.com/api/where/metrics.json",
			"status_code": 404,
		},
		Level: sentry.LevelWarning,
	})

	event := onlyEvent(t, rec)
	if got := exceptionValue(t, event); got != "metrics fetch failed" {
		t.Errorf("exception value = %q, want %q", got, "metrics fetch failed")
	}
	if event.Level != sentry.LevelWarning {
		t.Errorf("level = %q, want %q", event.Level, sentry.LevelWarning)
	}
	for key, want := range map[string]string{
		"agency_id":   "40",
		"agency_name": "Sound Transit",
		"server_name": "test-server",
	} {
		if got := event.Tags[key]; got != want {
			t.Errorf("tag %q = %q, want %q", key, got, want)
		}
	}
	extra, ok := event.Contexts["extra"]
	if !ok {
		t.Fatalf("event has no extra context: %v", event.Contexts)
	}
	if got, want := extra["url"], "https://oba.example.com/api/where/metrics.json"; got != want {
		t.Errorf("extra url = %v, want %q", got, want)
	}
	if got, want := extra["status_code"], 404; got != want {
		t.Errorf("extra status_code = %v, want %d", got, want)
	}
}

func TestReportErrorWithSentryOptionsWithoutOptions(t *testing.T) {
	rec := CaptureSentry(t)

	ReportErrorWithSentryOptions(errors.New("plain failure"), SentryReportOptions{})

	event := onlyEvent(t, rec)
	if event.Level != sentry.LevelError {
		t.Errorf("level = %q, want %q when no level is given", event.Level, sentry.LevelError)
	}
	if _, ok := event.Contexts["extra"]; ok {
		t.Errorf("event has an extra context, want none: %v", event.Contexts["extra"])
	}
}

// Each report runs in its own scope, so tags from one report must not leak
// into the next one.
func TestReportErrorWithSentryOptionsDoesNotLeakTagsBetweenReports(t *testing.T) {
	rec := CaptureSentry(t)

	ReportErrorWithSentryOptions(errors.New("first"), SentryReportOptions{
		Tags:         map[string]string{"server_name": "first-server"},
		ExtraContext: map[string]interface{}{"url": "https://first.example.com"},
	})
	ReportErrorWithSentryOptions(errors.New("second"), SentryReportOptions{})

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("captured %d Sentry events, want 2", len(events))
	}
	second := events[1]
	if got, ok := second.Tags["server_name"]; ok {
		t.Errorf("second report kept server_name tag %q from the first", got)
	}
	if _, ok := second.Contexts["extra"]; ok {
		t.Errorf("second report kept the first report's extra context: %v", second.Contexts["extra"])
	}
}

func TestConfigureScopeSetsRuntimeTagsAndHostname(t *testing.T) {
	rec := CaptureSentry(t)
	// ConfigureScope changes the hub's current scope. Push a scope so the
	// tags don't leak into other tests in this package.
	hub := sentry.CurrentHub()
	hub.PushScope()
	t.Cleanup(func() { hub.PopScope() })

	ConfigureScope("testing", "v1.2.3")
	ReportError(errors.New("after configure"))

	event := onlyEvent(t, rec)
	for key, want := range map[string]string{
		"env":         "testing",
		"app_version": "v1.2.3",
		"go_version":  runtime.Version(),
		"goarch":      runtime.GOARCH,
	} {
		if got := event.Tags[key]; got != want {
			t.Errorf("tag %q = %q, want %q", key, got, want)
		}
	}
	hostInfo, ok := event.Contexts["host_info"]
	if !ok {
		t.Fatalf("event has no host_info context: %v", event.Contexts)
	}
	if got, want := hostInfo["hostname"], getHostname(); got != want {
		t.Errorf("host_info hostname = %v, want %q", got, want)
	}
}

func TestGetHostname(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		want = "unknown"
	}
	if got := getHostname(); got != want {
		t.Errorf("getHostname() = %q, want %q", got, want)
	}
}
