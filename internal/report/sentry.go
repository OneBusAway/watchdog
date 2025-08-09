package report

import (
	"log"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

// SetupSentry initializes the Sentry client using environment configuration.
// It enables tracing and debugging, sets the sample rate to 100%, and captures
// a startup message indicating that the Watchdog has started.
func SetupSentry() {
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              os.Getenv("SENTRY_DSN"),
		EnableTracing:    true,
		Debug:            true,
		TracesSampleRate: 1.0,
	}); err != nil {
		log.Fatalf("sentry.Init: %s", err)
	}
	sentry.CaptureMessage("Watchdog started")
}

// FlushSentry flushes any buffered Sentry events.
// It waits up to 2 seconds for all events to be delivered before shutting down.
func FlushSentry() {
	sentry.Flush(2 * time.Second)
}
