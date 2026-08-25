package config

import (
	"testing"
	"time"
)

func TestBackoffStore(t *testing.T) {
	store := NewBackoffStore()
	agencyID := "agency-42"

	t.Run("NextRetryAt returns false when no entry", func(t *testing.T) {
		_, ok := store.NextRetryAt(agencyID)
		if ok {
			t.Errorf("expected no backoff entry, got ok=true")
		}
	})

	t.Run("UpdateBackoff creates new entry", func(t *testing.T) {
		store.UpdateBackoff(agencyID)
		got, ok := store.NextRetryAt(agencyID)
		if !ok {
			t.Fatalf("expected entry to exist after UpdateBackoff")
		}

		now := time.Now().UTC()
		if got.Before(now.Add(BASE_BACKOFF)) {
			t.Errorf("expected next retry >= %v, got %v", now.Add(BASE_BACKOFF), got)
		}
	})

	t.Run("UpdateBackoff increases delay", func(t *testing.T) {
		before, _ := store.NextRetryAt(agencyID)
		store.UpdateBackoff(agencyID)
		after, _ := store.NextRetryAt(agencyID)

		if !after.After(before) {
			t.Errorf("expected NextRetryAt to move forward after backoff increase, got before=%v after=%v", before, after)
		}
	})

	t.Run("ResetBackoff deletes entry", func(t *testing.T) {
		store.ResetBackoff(agencyID)
		_, ok := store.NextRetryAt(agencyID)
		if ok {
			t.Errorf("expected no entry after ResetBackoff")
		}
	})
}
