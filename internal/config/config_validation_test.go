package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
)

// validServer returns a fully-populated server that should pass validation.
func validServer() models.ObaServer {
	return models.ObaServer{
		Name:               "Test Server",
		ID:                 1,
		ObaBaseURL:         "https://test.example.com",
		ObaApiKey:          "test-key",
		GtfsUrl:            "https://gtfs.example.com",
		TripUpdateUrl:      "https://trip.example.com",
		VehiclePositionUrl: "https://vehicle.example.com",
		GtfsRtApiKey:       "",
		GtfsRtApiValue:     "",
		AgencyID:           "agency-1",
	}
}

func TestValidateServer(t *testing.T) {
	t.Run("valid server passes", func(t *testing.T) {
		if err := ValidateServer(validServer()); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	t.Run("empty optional GTFS-RT auth fields are allowed", func(t *testing.T) {
		s := validServer()
		s.GtfsRtApiKey = ""
		s.GtfsRtApiValue = ""
		s.TripUpdateUrl = ""
		if err := ValidateServer(s); err != nil {
			t.Fatalf("expected no error for empty optional fields, got: %v", err)
		}
	})

	requiredFieldCases := []struct {
		name      string
		mutate    func(*models.ObaServer)
		wantField string
	}{
		{"missing gtfs_url", func(s *models.ObaServer) { s.GtfsUrl = "" }, "gtfs_url"},
		{"missing vehicle_position_url", func(s *models.ObaServer) { s.VehiclePositionUrl = "" }, "vehicle_position_url"},
		{"missing oba_base_url", func(s *models.ObaServer) { s.ObaBaseURL = "" }, "oba_base_url"},
		{"missing oba_api_key", func(s *models.ObaServer) { s.ObaApiKey = "" }, "oba_api_key"},
		{"missing agency_id", func(s *models.ObaServer) { s.AgencyID = "" }, "agency_id"},
		{"missing name", func(s *models.ObaServer) { s.Name = "" }, "name"},
		{"missing id", func(s *models.ObaServer) { s.ID = 0 }, "id"},
		{"whitespace-only agency_id", func(s *models.ObaServer) { s.AgencyID = "   " }, "agency_id"},
		{"whitespace-only gtfs_url", func(s *models.ObaServer) { s.GtfsUrl = "  \t " }, "gtfs_url"},
	}

	for _, tc := range requiredFieldCases {
		t.Run(tc.name, func(t *testing.T) {
			s := validServer()
			tc.mutate(&s)
			err := ValidateServer(s)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("expected error to mention %q, got: %v", tc.wantField, err)
			}
		})
	}

	t.Run("reports all missing fields at once", func(t *testing.T) {
		// Mirrors the production config where every feed field was null.
		s := models.ObaServer{
			Name:       "Intercity Transit",
			ID:         33,
			ObaBaseURL: "https://intercity-transit-oba-server.onrender.com",
			ObaApiKey:  "org.onebusaway.iphone",
		}
		err := ValidateServer(s)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		for _, field := range []string{"gtfs_url", "vehicle_position_url", "agency_id"} {
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("expected error to mention %q, got: %v", field, err)
			}
		}
	})
}

// loadConfigFromFile must drop servers that fail validation (e.g. the
// production config where every feed URL was null) while keeping the valid
// ones, so one bad entry can't take down monitoring for the whole fleet.
func TestLoadConfigFromFileFiltersInvalidServers(t *testing.T) {
	content := `[
		{
			"name": "Valid Server", "id": 1,
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-1"
		},
		{
			"name": "Broken Server", "id": 2,
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_url": null,
			"vehicle_position_url": null,
			"agency_id": null
		}
	]`

	dir := t.TempDir()
	fp := filepath.Join(dir, "config.json")
	if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	servers, err := loadConfigFromFile(fp, NewDroppedServersStore())
	if err != nil {
		t.Fatalf("loadConfigFromFile failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("expected 1 valid server, got %d: %+v", len(servers), servers)
	}
	if servers[0].ID != 1 {
		t.Fatalf("expected the valid server (id 1) to be kept, got id %d", servers[0].ID)
	}
}

// loadConfigFromURL is an independent call site from loadConfigFromFile and is
// the production path (remote config + periodic refresh), so it needs its own
// coverage that invalid servers are filtered out.
func TestLoadConfigFromURLFiltersInvalidServers(t *testing.T) {
	body := `[
		{
			"name": "Valid Server", "id": 1,
			"oba_base_url": "https://valid.example.com",
			"oba_api_key": "valid-key",
			"gtfs_url": "https://gtfs.example.com",
			"vehicle_position_url": "https://vehicle.example.com",
			"agency_id": "agency-1"
		},
		{
			"name": "Broken Server", "id": 2,
			"oba_base_url": "https://broken.example.com",
			"oba_api_key": "broken-key",
			"gtfs_url": null,
			"vehicle_position_url": null,
			"agency_id": null
		}
	]`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer ts.Close()

	servers, err := loadConfigFromURL(context.Background(), &http.Client{Timeout: 10 * time.Second}, ts.URL, "", "", NewDroppedServersStore(), 1)
	if err != nil {
		t.Fatalf("loadConfigFromURL failed: %v", err)
	}

	if len(servers) != 1 || servers[0].ID != 1 {
		t.Fatalf("expected only the valid server (id 1), got %+v", servers)
	}
}

func TestReconcile(t *testing.T) {
	t.Run("drops every invalid server and preserves the order of valid ones", func(t *testing.T) {
		store := NewDroppedServersStore()
		valid1 := validServer()
		valid1.ID = 1
		valid2 := validServer()
		valid2.ID = 2
		invalidA := validServer()
		invalidA.ID = 10
		invalidA.GtfsUrl = ""
		invalidB := validServer()
		invalidB.ID = 11
		invalidB.AgencyID = ""

		// Interleave valid and invalid: valid, invalid, valid, invalid.
		got := store.Reconcile([]models.ObaServer{valid1, invalidA, valid2, invalidB})

		if len(got) != 2 {
			t.Fatalf("expected 2 valid servers, got %d: %+v", len(got), got)
		}
		if got[0].ID != 1 || got[1].ID != 2 {
			t.Fatalf("expected valid servers kept in order (1, 2), got (%d, %d)", got[0].ID, got[1].ID)
		}
	})

	t.Run("all servers invalid yields an empty slice", func(t *testing.T) {
		store := NewDroppedServersStore()
		bad := validServer()
		bad.GtfsUrl = ""
		got := store.Reconcile([]models.ObaServer{bad})
		if len(got) != 0 {
			t.Fatalf("expected 0 valid servers, got %d", len(got))
		}
	})

	t.Run("empty input yields an empty slice", func(t *testing.T) {
		store := NewDroppedServersStore()
		got := store.Reconcile(nil)
		if len(got) != 0 {
			t.Fatalf("expected 0 servers, got %d", len(got))
		}
	})
}

// A server that stays invalid across refresh cycles must be reported to Sentry
// exactly once, not once per cycle.
func TestReconcileReportsInvalidServerOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	invalid := validServer()
	invalid.ID = 7
	invalid.GtfsUrl = ""

	for i := 0; i < 3; i++ {
		if got := store.Reconcile([]models.ObaServer{invalid}); len(got) != 0 {
			t.Fatalf("iteration %d: expected invalid server dropped, got %d valid", i, len(got))
		}
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 Sentry report for a persistently invalid server, got %d", len(events))
	}
	if events[0].Level != sentry.LevelError {
		t.Errorf("expected error level, got %s", events[0].Level)
	}
	if events[0].Tags["server_id"] != "7" || events[0].Tags["server_name"] != invalid.Name {
		t.Errorf("unexpected tags: %v", events[0].Tags)
	}
}

// When a previously dropped server becomes valid, exactly one info-level
// recovery report is emitted and no further invalid reports follow.
func TestReconcileReportsRecoveryOnce(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	invalid := validServer()
	invalid.ID = 7
	invalid.GtfsUrl = ""

	if got := store.Reconcile([]models.ObaServer{invalid}); len(got) != 0 {
		t.Fatal("expected invalid server dropped")
	}

	recovered := invalid
	recovered.GtfsUrl = "https://gtfs.example.com"
	for i := 0; i < 2; i++ {
		if got := store.Reconcile([]models.ObaServer{recovered}); len(got) != 1 {
			t.Fatalf("iteration %d: expected recovered server kept, got %d valid", i, len(got))
		}
	}

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("expected 1 error + 1 recovery report, got %d", len(events))
	}
	if events[0].Level != sentry.LevelError {
		t.Errorf("expected first event at error level, got %s", events[0].Level)
	}
	if events[1].Level != sentry.LevelInfo {
		t.Errorf("expected recovery event at info level, got %s", events[1].Level)
	}
}

// A reported server that disappears from the config is pruned; if it later
// reappears invalid, it is a fresh state and gets reported again.
func TestReconcileReportsPrunedServerAgain(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	invalid := validServer()
	invalid.ID = 7
	invalid.GtfsUrl = ""

	if got := store.Reconcile([]models.ObaServer{invalid}); len(got) != 0 {
		t.Fatal("expected invalid server dropped")
	}

	if got := store.Reconcile(nil); len(got) != 0 {
		t.Fatal("expected no servers")
	}

	if got := store.Reconcile([]models.ObaServer{invalid}); len(got) != 0 {
		t.Fatal("expected invalid server dropped")
	}

	if events := rec.Events(); len(events) != 2 {
		t.Fatalf("expected 2 Sentry reports, got %d", len(events))
	}
}

// Sentry reports for dropped servers must carry only identifying tags, never
// credentials such as API keys.
func TestReconcileReportsNoCredentials(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	invalid := validServer()
	invalid.ID = 7
	invalid.ObaApiKey = "super-secret-key"
	invalid.GtfsRtApiKey = "gtfs-secret"
	invalid.GtfsRtApiValue = "gtfs-value"
	invalid.GtfsUrl = ""

	store.Reconcile([]models.ObaServer{invalid})

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 report, got %d", len(events))
	}
	event := events[0]

	for _, secret := range []string{"super-secret-key", "gtfs-secret", "gtfs-value"} {
		if strings.Contains(event.Message, secret) {
			t.Errorf("report leaked credential %q in message", secret)
		}
		if len(event.Exception) > 0 && strings.Contains(event.Exception[0].Value, secret) {
			t.Errorf("report leaked credential %q in exception value", secret)
		}
		for _, tag := range event.Tags {
			if strings.Contains(tag, secret) {
				t.Errorf("report leaked credential %q in tag", secret)
			}
		}
	}

	for _, want := range []string{"server_id", "server_name"} {
		if _, ok := event.Tags[want]; !ok {
			t.Errorf("expected tag %q to be present, got %v", want, event.Tags)
		}
	}
}

func TestRejectDuplicateServerIDs(t *testing.T) {
	t.Run("keeps the first server per ID and preserves order", func(t *testing.T) {
		rec := report.CaptureSentry(t)
		store := NewDroppedServersStore()

		a := validServer()
		a.ID = 1
		b := validServer()
		b.ID = 2
		dup := validServer()
		dup.ID = 1
		dup.Name = "Server A Duplicate"
		d := validServer()
		d.ID = 3

		got := store.rejectDuplicateServerIDs([]models.ObaServer{a, b, dup, d})

		if len(got) != 3 {
			t.Fatalf("expected 3 unique servers, got %d: %+v", len(got), got)
		}
		if got[0].ID != 1 || got[0].Name != a.Name {
			t.Errorf("expected first occurrence of id 1 kept, got %+v", got[0])
		}
		if got[1].ID != 2 || got[2].ID != 3 {
			t.Errorf("expected order preserved (2, 3), got (%d, %d)", got[1].ID, got[2].ID)
		}
		if events := rec.Events(); len(events) != 1 {
			t.Fatalf("expected 1 duplicate report, got %d", len(events))
		}
	})

	t.Run("empty input yields an empty slice and no reports", func(t *testing.T) {
		rec := report.CaptureSentry(t)
		store := NewDroppedServersStore()
		if got := store.rejectDuplicateServerIDs(nil); len(got) != 0 {
			t.Fatalf("expected 0 servers, got %d", len(got))
		}
		if events := rec.Events(); len(events) != 0 {
			t.Fatalf("expected 0 reports, got %d", len(events))
		}
	})

	t.Run("reports each duplicate ID only once across cycles", func(t *testing.T) {
		rec := report.CaptureSentry(t)
		store := NewDroppedServersStore()

		dup := validServer()
		dup.ID = 7
		dup.Name = "Server 7 Duplicate"
		servers := []models.ObaServer{validServer(), dup, dup}

		if got := store.rejectDuplicateServerIDs(servers); len(got) != 2 {
			t.Fatalf("expected 2 unique servers, got %d", len(got))
		}
		if got := store.rejectDuplicateServerIDs(servers); len(got) != 2 {
			t.Fatalf("expected 2 unique servers, got %d", len(got))
		}

		if events := rec.Events(); len(events) != 1 {
			t.Fatalf("expected 1 duplicate report, got %d", len(events))
		}
	})

	t.Run("prunes duplicate IDs that leave the config so they report again", func(t *testing.T) {
		rec := report.CaptureSentry(t)
		store := NewDroppedServersStore()

		dup := validServer()
		dup.ID = 7
		dup.Name = "Server 7 Duplicate"

		store.rejectDuplicateServerIDs([]models.ObaServer{validServer(), dup, dup})
		if events := rec.Events(); len(events) != 1 {
			t.Fatalf("expected 1 duplicate report, got %d", len(events))
		}

		store.rejectDuplicateServerIDs([]models.ObaServer{validServer()})
		store.rejectDuplicateServerIDs([]models.ObaServer{validServer(), dup, dup})

		if events := rec.Events(); len(events) != 2 {
			t.Fatalf("expected duplicate re-reported after pruning, got %d", len(events))
		}
	})

	t.Run("reports carry identifying tags and never credentials", func(t *testing.T) {
		rec := report.CaptureSentry(t)
		store := NewDroppedServersStore()

		dup := validServer()
		dup.ID = 7
		dup.ObaApiKey = "super-secret-key"
		dup.GtfsRtApiKey = "gtfs-secret"
		dup.GtfsRtApiValue = "gtfs-value"

		got := store.rejectDuplicateServerIDs([]models.ObaServer{dup, dup})
		if len(got) != 1 {
			t.Fatalf("expected 1 unique server, got %d", len(got))
		}

		events := rec.Events()
		if len(events) != 1 {
			t.Fatalf("expected 1 report, got %d", len(events))
		}
		event := events[0]
		if event.Level != sentry.LevelError {
			t.Errorf("expected error level, got %s", event.Level)
		}
		if event.Tags["server_id"] != "7" || event.Tags["server_name"] != dup.Name {
			t.Errorf("unexpected tags: %v", event.Tags)
		}

		for _, secret := range []string{"super-secret-key", "gtfs-secret", "gtfs-value"} {
			if strings.Contains(event.Message, secret) {
				t.Errorf("report leaked credential %q in message", secret)
			}
			if len(event.Exception) > 0 && strings.Contains(event.Exception[0].Value, secret) {
				t.Errorf("report leaked credential %q in exception value", secret)
			}
			for _, tag := range event.Tags {
				if strings.Contains(tag, secret) {
					t.Errorf("report leaked credential %q in tag", secret)
				}
			}
		}
	})
}

// Two invalid servers sharing a non-zero ID must both surface to Sentry: the
// second one via the duplicate-ID rejection and the first via Reconcile.
// Without the pre-pass, Reconcile's report-once state (keyed by server ID)
// would silently swallow the second one.
func TestReconcileDuplicateInvalidServersBothReported(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	invalidA := validServer()
	invalidA.ID = 7
	invalidA.GtfsUrl = ""
	invalidB := validServer()
	invalidB.ID = 7
	invalidB.AgencyID = ""

	servers := []models.ObaServer{invalidA, invalidB}
	if got := store.Reconcile(store.rejectDuplicateServerIDs(servers)); len(got) != 0 {
		t.Fatalf("expected both invalid servers dropped, got %d valid", len(got))
	}

	if events := rec.Events(); len(events) != 2 {
		t.Fatalf("expected 2 Sentry reports (1 duplicate + 1 invalid), got %d", len(events))
	}

	// Persistent duplicates must stay silent on subsequent refresh cycles.
	if got := store.Reconcile(store.rejectDuplicateServerIDs(servers)); len(got) != 0 {
		t.Fatalf("expected both invalid servers dropped, got %d valid", len(got))
	}
	if events := rec.Events(); len(events) != 2 {
		t.Fatalf("expected no new Sentry reports on re-reconcile, got %d", len(events))
	}
}

// A valid server followed by a same-ID duplicate must keep the valid server and
// report only the duplicate — never a bogus "recovered" event, since the
// duplicate report is tracked separately from Reconcile's invalid-report state.
func TestReconcileValidServerWithDuplicateNoBogusRecovery(t *testing.T) {
	rec := report.CaptureSentry(t)
	store := NewDroppedServersStore()

	valid := validServer()
	valid.ID = 5
	dup := validServer()
	dup.ID = 5
	dup.Name = "Server 5 Duplicate"

	servers := []models.ObaServer{valid, dup}
	got := store.Reconcile(store.rejectDuplicateServerIDs(servers))
	if len(got) != 1 || got[0].ID != 5 || got[0].Name != valid.Name {
		t.Fatalf("expected the first server (id 5) kept, got %+v", got)
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 duplicate report, got %d", len(events))
	}
	if events[0].Level != sentry.LevelError {
		t.Errorf("expected duplicate report at error level, got %s", events[0].Level)
	}
	if strings.Contains(events[0].Message, "recovered") {
		t.Errorf("duplicate report must not be misread as a recovery: %s", events[0].Message)
	}

	// A second cycle stays silent: no duplicate, and no bogus recovery.
	if got := store.Reconcile(store.rejectDuplicateServerIDs(servers)); len(got) != 1 {
		t.Fatalf("expected the first server (id 5) kept, got %+v", got)
	}
	if events := rec.Events(); len(events) != 1 {
		t.Fatalf("expected no new reports on re-reconcile, got %d", len(events))
	}
}

func TestLoadConfigFromFileRejectsDuplicateIDs(t *testing.T) {
	content := `[
		{
			"name": "Server A", "id": 1,
			"oba_base_url": "https://a.example.com",
			"oba_api_key": "key-a",
			"gtfs_url": "https://gtfs-a.example.com",
			"vehicle_position_url": "https://vehicle-a.example.com",
			"agency_id": "agency-a"
		},
		{
			"name": "Server A Duplicate", "id": 1,
			"oba_base_url": "https://a.example.com",
			"oba_api_key": "key-a",
			"gtfs_url": "https://gtfs-a.example.com",
			"vehicle_position_url": "https://vehicle-a.example.com",
			"agency_id": "agency-a"
		}
	]`

	dir := t.TempDir()
	fp := filepath.Join(dir, "config.json")
	if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	servers, err := loadConfigFromFile(fp, NewDroppedServersStore())
	if err != nil {
		t.Fatalf("loadConfigFromFile failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("expected 1 unique server, got %d: %+v", len(servers), servers)
	}
	if servers[0].ID != 1 || servers[0].Name != "Server A" {
		t.Fatalf("expected the first server with id 1 kept, got %+v", servers[0])
	}
}

func TestLoadConfigFromURLRejectsDuplicateIDs(t *testing.T) {
	body := `[
		{
			"name": "Server A", "id": 1,
			"oba_base_url": "https://a.example.com",
			"oba_api_key": "key-a",
			"gtfs_url": "https://gtfs-a.example.com",
			"vehicle_position_url": "https://vehicle-a.example.com",
			"agency_id": "agency-a"
		},
		{
			"name": "Server A Duplicate", "id": 1,
			"oba_base_url": "https://a.example.com",
			"oba_api_key": "key-a",
			"gtfs_url": "https://gtfs-a.example.com",
			"vehicle_position_url": "https://vehicle-a.example.com",
			"agency_id": "agency-a"
		}
	]`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer ts.Close()

	servers, err := loadConfigFromURL(context.Background(), &http.Client{Timeout: 10 * time.Second}, ts.URL, "", "", NewDroppedServersStore(), 1)
	if err != nil {
		t.Fatalf("loadConfigFromURL failed: %v", err)
	}

	if len(servers) != 1 {
		t.Fatalf("expected 1 unique server, got %d: %+v", len(servers), servers)
	}
	if servers[0].ID != 1 || servers[0].Name != "Server A" {
		t.Fatalf("expected the first server with id 1 kept, got %+v", servers[0])
	}
}
