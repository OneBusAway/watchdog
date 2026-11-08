package investigation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func TestAPITime_UTC(t *testing.T) {
	local := time.Date(2026, 10, 24, 16, 5, 30, 120_000_000, time.FixedZone("EET", 2*60*60))

	if got := mustJSON(t, newAPITime(local)); got != `"2026-10-24T14:05:30.12Z"` {
		t.Errorf("apiTime = %s, want the same instant in UTC", got)
	}
}

func TestAPITime_NullIsPresent(t *testing.T) {
	type episode struct {
		OpenedAt apiTime  `json:"opened_at"`
		ClosedAt *apiTime `json:"closed_at"`
	}
	opened := time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC)

	open := mustJSON(t, episode{OpenedAt: newAPITime(opened), ClosedAt: newOptionalAPITime(nil)})
	if open != `{"opened_at":"2026-10-24T14:00:00Z","closed_at":null}` {
		t.Errorf("open episode = %s, want closed_at present as null", open)
	}

	closedAt := opened.Add(time.Hour)
	closed := mustJSON(t, episode{OpenedAt: newAPITime(opened), ClosedAt: newOptionalAPITime(&closedAt)})
	if closed != `{"opened_at":"2026-10-24T14:00:00Z","closed_at":"2026-10-24T15:00:00Z"}` {
		t.Errorf("closed episode = %s", closed)
	}
}

func TestAPIDate(t *testing.T) {
	date := pgtype.Date{Time: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), Valid: true}

	if got := mustJSON(t, newAPIDate(date)); got != `"2026-10-24"` {
		t.Errorf("apiDate = %s, want \"2026-10-24\"", got)
	}
	if got := mustJSON(t, newAPIDate(pgtype.Date{})); got != "null" {
		t.Errorf("null date = %s, want null", got)
	}
	infinite := pgtype.Date{InfinityModifier: pgtype.Infinity, Valid: true}
	if got := mustJSON(t, newAPIDate(infinite)); got != "null" {
		t.Errorf("infinite date = %s, want null", got)
	}
}
