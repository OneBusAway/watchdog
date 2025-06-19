package report

import (
	"os"
	"runtime"

	"github.com/getsentry/sentry-go"
)

// ConfigureScope sets global Sentry scope tags and context related to the runtime and host.
func ConfigureScope(env, version string) {
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("env", env)
		scope.SetTag("app_version", version)
		scope.SetTag("go_version", runtime.Version())
		scope.SetTag("goarch", runtime.GOARCH)
		scope.SetContext("host_info", map[string]interface{}{
			"hostname": getHostname(),
		})
	})
}

// getHostname retrieves the system hostname.
// If the hostname cannot be determined, it returns "unknown".
func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

// ReportError reports the error to Sentry with the given severity level
// If no level is provided, it defaults to sentry.LevelError.
func ReportError(err error, levels ...sentry.Level) {
	if err == nil {
		return
	}

	level := sentry.LevelError
	if len(levels) > 0 {
		level = levels[0]
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(level)
		sentry.CaptureException(err)
	})
}

// SentryReportOptions provides optional data for reporting.
type SentryReportOptions struct {
	ExtraContext map[string]interface{}
	Tags         map[string]string
	Level        sentry.Level
}

// ReportErrorWithSentryOptions reports the error with additional options (tags, context, level).
func ReportErrorWithSentryOptions(err error, opts SentryReportOptions) {
	if err == nil {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		if opts.ExtraContext != nil {
			scope.SetContext("extra", opts.ExtraContext)
		}
		if opts.Tags != nil {
			for k, v := range opts.Tags {
				scope.SetTag(k, v)
			}
		}
		if opts.Level != "" {
			scope.SetLevel(opts.Level)
		}
		sentry.CaptureException(err)
	})
}
