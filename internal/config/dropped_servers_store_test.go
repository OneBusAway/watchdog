package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/report"
)

func TestDroppedServersStore_SharedIdentityRecovery(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	broken := validServer()
	broken.ServerName = "bad"
	broken.GtfsStaticFeeds = nil

	valid := validServer()
	valid.ServerName = "a"

	// Cycle 1: broken ("bad") and valid ("a") share an identity in the same config.
	got := store.Reconcile([]json.RawMessage{
		mustRawServer(t, broken),
		mustRawServer(t, valid),
	}, testLogger())
	if len(got) != 1 || got[0].ServerName != "a" {
		t.Fatalf("expected valid server 'a' to be kept, got %+v", got)
	}

	// Cycle 2: the broken entry is removed, triggering recovery for the shared identity.
	got = store.Reconcile([]json.RawMessage{
		mustRawServer(t, valid),
	}, testLogger())
	if len(got) != 1 || got[0].ServerName != "a" {
		t.Fatalf("expected valid server 'a' to be kept, got %+v", got)
	}

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("expected 1 error + 1 recovery event, got %d", len(events))
	}

	recovery := events[1]
	if recovery.Level != sentry.LevelInfo {
		t.Errorf("expected info level for recovery event, got %s", recovery.Level)
	}
	if recovery.Tags["server_name"] != "bad" {
		t.Errorf("expected recovery server_name tag to be %q, got %q", "bad", recovery.Tags["server_name"])
	}

	msg := recovery.Message
	if msg == "" && len(recovery.Exception) > 0 {
		msg = recovery.Exception[0].Value
	}
	if !strings.Contains(msg, `"bad"`) {
		t.Errorf("expected recovery message to name %q, got %q", "bad", msg)
	}
}
