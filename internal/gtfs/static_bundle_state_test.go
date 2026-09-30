package gtfs

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestStaticBundleStateDistinguishesCurrentStaleAndExpired(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	server := models.ObaServer{AgencyID: "a", ObaBaseURL: "https://oba.example"}
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStaticStore()
	store.ScheduleStore().Replace(server, map[string]*ScheduleSnapshot{
		server.ServerKey(): {
			timezone: location,
			services: []scheduleService{{end: 20260929}},
			complete: true,
		},
	})
	store.SetFetchTime(server.ServerKey(), now.Add(-time.Hour))
	store.SetRefreshState(server, StaticRefreshState{LastAttempt: now, Success: true})

	current, usable := store.BundleState(server, now, 26*time.Hour)
	if !current || !usable {
		t.Fatalf("fresh state = current %v usable %v, want true true", current, usable)
	}

	store.SetRefreshState(server, StaticRefreshState{LastAttempt: now, Retrying: true})
	current, usable = store.BundleState(server, now, 26*time.Hour)
	if current || !usable {
		t.Fatalf("retained state = current %v usable %v, want false true", current, usable)
	}

	current, usable = store.BundleState(server, now.Add(24*time.Hour), 26*time.Hour)
	if current || usable {
		t.Fatalf("expired state = current %v usable %v, want false false", current, usable)
	}
}

func TestScheduleCoverageIsPerAgencyInServerMode(t *testing.T) {
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	location := time.UTC
	server := models.ObaServer{ObaBaseURL: "https://oba.example"}
	store := NewScheduleStore()
	store.Replace(server, map[string]*ScheduleSnapshot{
		models.ServerKey(server.ObaBaseURL, "expired"): {timezone: location, services: []scheduleService{{end: 20260928}}, complete: true},
		models.ServerKey(server.ObaBaseURL, "current"): {timezone: location, services: []scheduleService{{end: 20261015}}, complete: true},
	})

	if store.CoversDate(models.ServerKey(server.ObaBaseURL, "expired"), now) {
		t.Fatal("expired agency inherited another agency's later service date")
	}
	if !store.CoversDate(models.ServerKey(server.ObaBaseURL, "current"), now) {
		t.Fatal("current agency was not usable")
	}
	earliest, latest, ok := store.ServiceDateRange(models.ServerKey(server.ObaBaseURL, "current"))
	if !ok || earliest.Format("20060102") != "20261015" || latest.Format("20060102") != "20261015" {
		t.Fatalf("unexpected service range: %v %v %v", earliest, latest, ok)
	}
}

func TestReplaceServerSnapshotRetiresRemovedAgencies(t *testing.T) {
	server := models.ObaServer{ObaBaseURL: "https://oba.example"}
	store := NewStaticStore()
	first := &models.StaticData{}
	store.ReplaceServerSnapshot(server, map[string]*models.StaticData{
		models.ServerKey(server.ObaBaseURL, "a"): first,
		models.ServerKey(server.ObaBaseURL, "b"): first,
	}, time.Now())

	removed := store.ReplaceServerSnapshot(server, map[string]*models.StaticData{
		models.ServerKey(server.ObaBaseURL, "a"): {},
	}, time.Now())

	if len(removed) != 1 || removed[0] != models.ServerKey(server.ObaBaseURL, "b") {
		t.Fatalf("removed = %v, want agency b", removed)
	}
	if _, ok := store.Get(models.ServerKey(server.ObaBaseURL, "b")); ok {
		t.Fatal("removed agency remained in static store")
	}
}
