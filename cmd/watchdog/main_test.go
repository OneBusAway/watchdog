package main

import (
	"fmt"
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
