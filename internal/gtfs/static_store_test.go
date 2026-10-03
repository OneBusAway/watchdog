package gtfs

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestIsConfiguredIgnoresNonStaticFields(t *testing.T) {
	server := models.ObaServer{ServerName: "s", AgencyID: "a", ObaBaseURL: "https://oba.example", ObaApiKey: "old", GtfsStaticFeeds: []string{"https://feed.example/a.zip"}}
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{server})

	rotated := server
	rotated.ObaApiKey = "new"
	if !store.IsConfigured(rotated) {
		t.Fatal("an API-key-only change must not unconfigure the static entry")
	}

	changedFeeds := server
	changedFeeds.GtfsStaticFeeds = []string{"https://feed.example/b.zip"}
	if store.IsConfigured(changedFeeds) {
		t.Fatal("a static feed change must unconfigure the stale entry")
	}
}

func TestConfiguredServersAreSortedByServerKey(t *testing.T) {
	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{
		{AgencyID: "c", ObaBaseURL: "https://oba.example"},
		{AgencyID: "a", ObaBaseURL: "https://oba.example"},
		{AgencyID: "b", ObaBaseURL: "https://oba.example"},
	})
	for range 10 {
		servers := store.ConfiguredServers()
		for i := 1; i < len(servers); i++ {
			if servers[i-1].ServerKey() >= servers[i].ServerKey() {
				t.Fatalf("servers not sorted: %v", servers)
			}
		}
	}
}

func TestServerScopedCleanupPreservesConfiguredAgencyEntries(t *testing.T) {
	serverScoped := models.ObaServer{ServerName: "server", ObaBaseURL: "https://oba.example"}
	agencyScoped := models.ObaServer{ServerName: "agency", AgencyID: "solo", ObaBaseURL: "https://oba.example"}
	agencyKey := agencyScoped.ServerKey()
	discoveredKey := models.ServerKey(serverScoped.ObaBaseURL, "gone")
	now := time.Now().UTC()

	store := NewStaticStore()
	store.SetConfiguredServers([]models.ObaServer{serverScoped, agencyScoped})
	store.ReplaceServerSnapshot(agencyScoped, map[string]*models.StaticData{agencyKey: {}}, now)
	store.ReplaceServerSnapshot(serverScoped, map[string]*models.StaticData{discoveredKey: {}}, now)
	store.ScheduleStore().Replace(agencyScoped, map[string]*ScheduleSnapshot{agencyKey: {complete: true}})
	store.ScheduleStore().Replace(serverScoped, map[string]*ScheduleSnapshot{discoveredKey: {complete: true}})

	removed := store.ReplaceServerSnapshot(serverScoped, map[string]*models.StaticData{}, now)
	if len(removed) != 1 || removed[0] != discoveredKey {
		t.Fatalf("removed = %v, want only %s", removed, discoveredKey)
	}
	if _, ok := store.Get(agencyKey); !ok {
		t.Fatal("server-scoped replacement removed a separately configured agency snapshot")
	}

	store.ScheduleStore().MarkUnavailable(serverScoped)
	if !store.ScheduleStore().data[agencyKey].available {
		t.Fatal("server-scoped MarkUnavailable touched a separately configured agency schedule")
	}
	store.ScheduleStore().Replace(serverScoped, nil)
	if _, ok := store.ScheduleStore().data[agencyKey]; !ok {
		t.Fatal("server-scoped schedule replacement removed a separately configured agency schedule")
	}
	if _, ok := store.ScheduleStore().data[discoveredKey]; ok {
		t.Fatal("server-scoped schedule replacement kept a retired discovered agency")
	}
}
