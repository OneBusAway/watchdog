package app

import (
	"log/slog"
	"net/http"
	"os"
	"sort"
	"testing"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/models"
)

// serverKeys is a readability helper for asserting on the newcomer slice.
func serverKeys(servers []models.ObaServer) []string {
	keys := make([]string, 0, len(servers))
	for _, server := range servers {
		keys = append(keys, server.ServerKey())
	}
	return keys
}

func assertServerKeys(t *testing.T, got []models.ObaServer, want ...string) {
	t.Helper()

	gotKeys := serverKeys(got)
	if len(gotKeys) != len(want) {
		t.Fatalf("expected newcomers %v, got %v", want, gotKeys)
	}
	for i, key := range want {
		if gotKeys[i] != key {
			t.Fatalf("expected newcomers %v, got %v", want, gotKeys)
		}
	}
}

// TestNewlyAddedServersIgnoresServersAlreadyInConfig is the Bug A regression
// test. refreshConfig invokes onUpdated on every successful load, not only when
// the config changed, so a server whose bundle download keeps failing (or a
// server-scoped entry whose feeds declare no agency, which stores nothing at
// all) must not be re-reported as a newcomer on every tick — that spawned a
// fresh DownloadGTFSBundles goroutine every minute with no in-flight guard.
func TestNewlyAddedServersIgnoresServersAlreadyInConfig(t *testing.T) {
	app := newTestApplication(t)

	existing := models.ObaServer{
		ServerName: "existing",
		ObaBaseURL: "https://existing.example.com",
		AgencyID:   "agency-a",
	}

	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{existing}), existing.ServerKey())

	// The bundle download failed, so nothing was ever written to the static
	// store. The next refresh carries the identical config and must report no
	// newcomers regardless.
	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{existing}))
}

// TestNewlyAddedServersDetectsNewAgencyOnKnownBaseURL is the Bug B regression
// test. An agency-scoped entry owns exactly one store key, so a second agency
// added on a base URL that is already configured is a genuine newcomer even
// though other keys under that base URL exist.
func TestNewlyAddedServersDetectsNewAgencyOnKnownBaseURL(t *testing.T) {
	app := newTestApplication(t)

	const sharedURL = "https://shared.example.com"
	agencyA := models.ObaServer{ServerName: "shared", ObaBaseURL: sharedURL, AgencyID: "agency-a"}
	agencyB := models.ObaServer{ServerName: "shared", ObaBaseURL: sharedURL, AgencyID: "agency-b"}

	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{agencyA}), agencyA.ServerKey())

	// agency-a downloaded its bundle successfully, so the base URL already owns
	// a key in the static store.
	app.GtfsService.StaticStore.Set(agencyA.ServerKey(), &models.StaticData{})

	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{agencyA, agencyB}), agencyB.ServerKey())
}

// TestNewlyAddedServersDetectsNewBaseURL covers the ordinary case: a
// server-scoped entry on a base URL that was not previously configured.
func TestNewlyAddedServersDetectsNewBaseURL(t *testing.T) {
	app := newTestApplication(t)

	existing := models.ObaServer{ServerName: "existing", ObaBaseURL: "https://existing.example.com", AgencyID: "agency-a"}
	added := models.ObaServer{ServerName: "added", ObaBaseURL: "https://added.example.com"}

	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{existing}), existing.ServerKey())
	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{existing, added}), added.ServerKey())
}

// TestNewlyAddedServersRedetectsAfterRemoval: a server that left the config had
// its state pruned, so if it comes back it needs its bundle downloaded again.
func TestNewlyAddedServersRedetectsAfterRemoval(t *testing.T) {
	app := newTestApplication(t)

	kept := models.ObaServer{ServerName: "kept", ObaBaseURL: "https://kept.example.com", AgencyID: "agency-a"}
	gone := models.ObaServer{ServerName: "gone", ObaBaseURL: "https://gone.example.com", AgencyID: "agency-b"}

	app.NewlyAddedServers([]models.ObaServer{kept, gone})
	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{kept}))
	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{kept, gone}), gone.ServerKey())
}

// TestNewlyAddedServersSeededFromBootConfig guards the start-up case: New must
// seed the known set from the boot configuration, otherwise the first config
// refresh after start-up re-downloads every bundle the process just fetched.
func TestNewlyAddedServersSeededFromBootConfig(t *testing.T) {
	booted := models.ObaServer{ServerName: "booted", ObaBaseURL: "https://booted.example.com", AgencyID: "agency-a"}
	cfg := config.NewConfig(4000, "testing", []models.ObaServer{booted})
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	app := New(cfg, logger, &http.Client{}, "test")

	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{booted}))

	added := models.ObaServer{ServerName: "added", ObaBaseURL: "https://booted.example.com", AgencyID: "agency-b"}
	assertServerKeys(t, app.NewlyAddedServers([]models.ObaServer{booted, added}), added.ServerKey())
}

// TestDepartedURLsRemembersEveryServerEverConfigured pins the accumulating
// half of KnownServerSet: unlike the newcomer diff, which replaces its set on
// every refresh, the departed-URL memory never forgets. A server reported once
// must keep being reported for as long as it is out of the config, because
// that repetition is what retires a series an in-flight collection tick
// re-created after it was pruned.
func TestDepartedURLsRemembersEveryServerEverConfigured(t *testing.T) {
	kept := models.ObaServer{ServerName: "kept", ObaBaseURL: "https://kept.example.com", AgencyID: "agency-a"}
	gone := models.ObaServer{ServerName: "gone", ObaBaseURL: "https://gone.example.com", AgencyID: "agency-b"}

	// Seeded from the boot configuration, as app.New does.
	set := NewKnownServerSet([]models.ObaServer{kept, gone})

	assertDepartedURLs(t, set.DepartedURLs([]models.ObaServer{kept, gone}))

	// gone leaves, and stays departed on every later refresh.
	assertDepartedURLs(t, set.DepartedURLs([]models.ObaServer{kept}), gone.ObaBaseURL)
	assertDepartedURLs(t, set.DepartedURLs([]models.ObaServer{kept}), gone.ObaBaseURL)

	// A server that comes back is not departed while it is configured.
	assertDepartedURLs(t, set.DepartedURLs([]models.ObaServer{kept, gone}))

	// A second agency on a base URL that is still configured is not a
	// departure: the URL-level memory is per base URL, so the server's own
	// series keep reporting and only the agency-level prune applies.
	sharedB := models.ObaServer{ServerName: "kept", ObaBaseURL: kept.ObaBaseURL, AgencyID: "agency-c"}
	set.DepartedURLs([]models.ObaServer{kept, sharedB, gone})
	assertDepartedURLs(t, set.DepartedURLs([]models.ObaServer{kept, gone}))
}

// TestDepartedURLsIgnoresBlankBaseURLs guards against a config entry with no
// oba_base_url poisoning the memory: it identifies no server, so remembering
// it would report a phantom departure on every refresh for the life of the
// process.
func TestDepartedURLsIgnoresBlankBaseURLs(t *testing.T) {
	set := NewKnownServerSet([]models.ObaServer{{ServerName: "blank", AgencyID: "agency-a"}})

	assertDepartedURLs(t, set.DepartedURLs(nil))
}

func assertDepartedURLs(t *testing.T, got []string, want ...string) {
	t.Helper()

	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("expected departed URLs %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected departed URLs %v, got %v", want, got)
		}
	}
}
