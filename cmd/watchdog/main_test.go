package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"watchdog.onebusaway.org/internal/report"
)

func TestReportInitialRemoteConfigNoValidServersSanitizesURL(t *testing.T) {
	rec := report.CaptureSentry(t)
	rawURL := "https://startup-user:startup-password@config.example.com/watchdog.json?token=startup-query-secret"
	safeURL := "https://config.example.com/watchdog.json"

	reportInitialRemoteConfigNoValidServers(rawURL, "startup-query-secret")

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 report, got %d", len(events))
	}
	event := events[0]
	if len(event.Exception) == 0 || !strings.Contains(event.Exception[0].Value, safeURL) {
		t.Errorf("exception does not retain sanitized URL %q: %v", safeURL, event.Exception)
	}
	if got := event.Tags["config_url"]; got != safeURL {
		t.Errorf("config_url tag = %q, want %q", got, safeURL)
	}

	for _, secret := range []string{"startup-user", "startup-password", "startup-query-secret"} {
		if strings.Contains(event.Message, secret) || strings.Contains(fmt.Sprint(event.Exception), secret) {
			t.Errorf("report leaked credential %q in message or exception", secret)
		}
		for _, tag := range event.Tags {
			if strings.Contains(tag, secret) {
				t.Errorf("report leaked credential %q in tag", secret)
			}
		}
		for contextName, context := range event.Contexts {
			for key, value := range context {
				if strings.Contains(fmt.Sprint(value), secret) {
					t.Errorf("report leaked credential %q in context %q field %q", secret, contextName, key)
				}
			}
		}
	}
}

func TestStartupValidationRejectsNonPositiveFetchInterval(t *testing.T) {
	for _, interval := range []string{"0", "-1"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestStartupValidationSubprocess", "--", "--fetch-interval="+interval, "--config-file=config.json")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		output, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Errorf("fetch-interval=%s: expected exit code 1, got error %v, output:\n%s", interval, err, string(output))
		}
		if !strings.Contains(string(output), "--fetch-interval must be greater than zero") {
			t.Errorf("fetch-interval=%s: output missing error message:\n%s", interval, string(output))
		}
		if strings.Contains(string(output), "panic:") {
			t.Errorf("fetch-interval=%s: output contains panic:\n%s", interval, string(output))
		}
	}
}

func TestStartupValidationSubprocess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = append([]string{"watchdog"}, args[i+1:]...)
			break
		}
	}
	os.Args = args
	main()
}
