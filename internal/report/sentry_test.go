package report_test

import (
	"os"
	"testing"

	"watchdog.onebusaway.org/internal/report"
)

func TestSetupSentry(t *testing.T) {
	t.Run("Valid DSN", func(t *testing.T) {
		os.Setenv("SENTRY_DSN", "https://public@sentry.example.com/1")
		defer os.Unsetenv("SENTRY_DSN")

		report.SetupSentry()
		report.FlushSentry()
	})
}
