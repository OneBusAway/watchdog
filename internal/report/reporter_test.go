package report

import (
	"errors"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestConfigureScope(t *testing.T) {
	ConfigureScope("test", "1.0.0")
	// Test it doesn't panic
}

func TestGetHostname(t *testing.T) {
	name := getHostname()
	if name == "" {
		t.Errorf("expected hostname to not be empty")
	}
}

func TestReportError(t *testing.T) {
	rec := CaptureSentry(t)
	ReportError(nil)
	ReportError(errors.New("test error"))
	ReportError(errors.New("test error with level"), sentry.LevelWarning)
	rec.Flush(0)
	rec.Configure(sentry.ClientOptions{})
	rec.Close()
	if len(rec.Events()) != 2 {
		t.Fatalf("expected 2 events")
	}
}

func TestReportErrorWithSentryOptions(t *testing.T) {
	rec := CaptureSentry(t)
	ReportErrorWithSentryOptions(nil, SentryReportOptions{})
	ReportErrorWithSentryOptions(errors.New("test options"), SentryReportOptions{
		ExtraContext: map[string]interface{}{"key": "value"},
		Tags:         map[string]string{"tag1": "value1"},
		Level:        sentry.LevelFatal,
	})
	if len(rec.Events()) != 1 {
		t.Fatalf("expected 1 event")
	}
}
