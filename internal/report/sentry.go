package report

import (
	"log"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

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


func FlushSentry() {
	sentry.Flush(2 * time.Second)
}
