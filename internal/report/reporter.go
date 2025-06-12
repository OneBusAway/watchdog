package report

import (
	"os"
	"runtime"

	"github.com/getsentry/sentry-go"
)

type Reporter struct {
	Env     string
	Version string
}

func NewReporter(env, version string) *Reporter {
	return &Reporter{
		Env:     env,
		Version: version,
	}
}

// ConfigureScope sets global Sentry scope tags and context related to the runtime and host.
func (r *Reporter) ConfigureScope() {
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("env", r.Env)
		scope.SetTag("version", r.Version)
		scope.SetTag("go_version", runtime.Version())
		scope.SetContext("host_info", map[string]interface{}{
			"hostname": r.getHostname(),
		})
	})
}

func (r *Reporter) getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}


// ReportIfProd sends the error to Sentry only if the environment is production.
// Optionally accepts a Sentry severity level (defaults to sentry.LevelError).

func (r *Reporter) ReportIfProd(err error, extraContext map[string]interface{}, levels ...sentry.Level) {
	if err == nil || r.Env != "production" {
		return
	}

	level := sentry.LevelError
	if len(levels) > 0 {
		level = levels[0]
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		if extraContext != nil {
			scope.SetContext("extra", extraContext)
		}
		scope.SetLevel(level)
		sentry.CaptureException(err)
	})
}
