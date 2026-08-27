//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
)

// TestDownloadGTFSBundles verifies that every configured server's GTFS static
// bundle can be downloaded, parsed, and stored.
//
// It drives DownloadGTFSBundles -- the same entry point main.go and the 24h
// refresh use -- rather than the single-feed DownloadGTFSBundle, because the
// interesting work is downstream of the fetch: multi-feed merging, agency
// discovery from agency.txt, per-agency key fan-out, bounding-box computation,
// and the route -> agency index the vehicle pass attributes through.
//
// That code path reports failures through the logger and Sentry rather than
// returning them, so the test installs a recording logger and fails on any
// error-level record. Without that, a server whose feeds all 404 -- or one
// where two of three feeds fail and the merge silently proceeds on the
// survivor -- would be indistinguishable from success.
func TestDownloadGTFSBundles(t *testing.T) {
	if len(integrationServers) == 0 {
		t.Skip("No servers found in config")
	}

	staticStore := gtfs.NewStaticStore()
	realtimeStore := gtfs.NewRealtimeStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	routeAgencyIndex := gtfs.NewRouteAgencyIndex()

	recorder := newErrorRecorder()
	logger := slog.New(recorder)
	// An integration test wants a fast, legible failure rather than production's
	// patience: DoWithBackoff caps at a 2m delay, so the default maxRetries of
	// 20 against an unreachable host would outlast `go test`'s 10m deadline and
	// surface as a goroutine-dump panic instead of a test failure.
	client := &http.Client{Timeout: 60 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// realtimeStore is unused here -- DownloadGTFSBundles never touches it --
	// but the constructor requires it. Live GTFS-RT fetching deserves its own
	// integration test.
	gtfsService := gtfs.NewGtfsService(staticStore, realtimeStore, boundingBoxStore, routeAgencyIndex, logger, client)

	// The download is hoisted out of the subtests and blocks until every server
	// is done, so the subtests below are pure in-memory assertions over what it
	// left behind -- no t.Parallel() needed.
	gtfsService.DownloadGTFSBundles(ctx, integrationServers, 3)

	if errs := recorder.Errors(); len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("DownloadGTFSBundles logged an error: %s", e)
		}
	}

	for _, server := range integrationServers {
		srv := server
		t.Run(subtestName(srv), func(t *testing.T) {
			if srv.IsServerScoped() {
				assertServerScopedBundle(t, srv, staticStore, boundingBoxStore, routeAgencyIndex)
				return
			}
			assertAgencyScopedBundle(t, srv, staticStore, boundingBoxStore)
		})
	}
}

// subtestName names a subtest by agency, falling back to the server for
// server-scoped entries, whose AgencyID is empty by definition.
func subtestName(srv models.ObaServer) string {
	if srv.IsServerScoped() {
		return fmt.Sprintf("Server_%s", srv.ServerName)
	}
	return fmt.Sprintf("Agency_%s", srv.AgencyID)
}

// assertAgencyScopedBundle checks the single key an agency-scoped entry owns,
// and that the configured agency_id is one the live feed actually declares.
func assertAgencyScopedBundle(t *testing.T, srv models.ObaServer, staticStore *gtfs.StaticStore, boundingBoxStore *geo.BoundingBoxStore) {
	t.Helper()

	data := assertBundleStored(t, srv.ServerKey(), staticStore, boundingBoxStore)

	// Agency-mode stores under the configured key regardless of what agency.txt
	// declares, so a typo'd or retired agency_id yields a perfectly healthy
	// bundle attached to an agency the feed has never heard of -- and empty OBA
	// API metrics in production. Only a live feed can catch this.
	var declared []string
	for _, agency := range data.Agencies {
		if agency.Id == srv.AgencyID {
			return
		}
		declared = append(declared, agency.Id)
	}
	t.Errorf("configured agency_id %q is not declared by the feed; agency.txt declares %v", srv.AgencyID, declared)
}

// assertBundleStored checks that a parsed, non-empty bundle and a bounding box
// both landed under serverKey, and returns the bundle for further assertions.
func assertBundleStored(t *testing.T, serverKey string, staticStore *gtfs.StaticStore, boundingBoxStore *geo.BoundingBoxStore) *models.StaticData {
	t.Helper()

	data, ok := staticStore.Get(serverKey)
	if !ok || data == nil {
		t.Fatalf("no GTFS static bundle stored under %s", serverKey)
	}
	if len(data.Stops) == 0 {
		t.Errorf("bundle stored under %s has no stops", serverKey)
	}
	if len(data.Routes) == 0 {
		t.Errorf("bundle stored under %s has no routes", serverKey)
	}
	if _, ok := boundingBoxStore.Get(serverKey); !ok {
		t.Errorf("no bounding box stored under %s", serverKey)
	}
	return data
}

// assertServerScopedBundle checks the fan-out a server-scoped entry performs:
// one static-store entry per agency declared in agency.txt, the server-scoped
// bounding box the vehicle pass reads, and a route index that can actually
// attribute a vehicle to an agency.
func assertServerScopedBundle(t *testing.T, srv models.ObaServer, staticStore *gtfs.StaticStore, boundingBoxStore *geo.BoundingBoxStore, routeAgencyIndex *gtfs.RouteAgencyIndex) {
	t.Helper()

	// OwnsServerKey prefix-matches on the base URL, so when a server-scoped and
	// an agency-scoped entry share an oba_base_url this also picks up the
	// latter's key. It cannot false-fail (that entry stores its own bundle),
	// but a failure may be reported under this subtest rather than its own.
	agencyKeys := make(map[string]bool)
	staticStore.Range(func(serverKey string, _ *models.StaticData) bool {
		if srv.OwnsServerKey(serverKey) {
			agencyKeys[serverKey] = true
		}
		return true
	})
	if len(agencyKeys) == 0 {
		t.Fatalf("server-scoped entry %s stored no per-agency bundles", srv.ObaBaseURL)
	}

	var sample *models.StaticData
	for key := range agencyKeys {
		if data := assertBundleStored(t, key, staticStore, boundingBoxStore); sample == nil {
			sample = data
		}
	}

	// The vehicle pass runs once per server and reads the agency-less key. Note
	// this is a bounding-box-only fan-out: no *static bundle* is stored under
	// that key, since blank-agency rows are skipped during the merge.
	if _, ok := boundingBoxStore.Get(models.ServerKey(srv.ObaBaseURL, "")); !ok {
		t.Errorf("server-scoped entry %s published no server-scoped bounding box", srv.ObaBaseURL)
	}

	// Server-mode attributes each RT vehicle to an agency through the route
	// index, keyed by the raw base URL rather than the sanitized one. Asserting
	// the key merely exists proves nothing -- storeStaticForServer calls Set
	// unconditionally, and Set registers even an empty map. What breaks the
	// vehicle pass is routes that resolve to no agency (agency_id is optional
	// in agency.txt for a single-agency feed), so resolve real route IDs.
	if sample == nil || len(sample.Routes) == 0 {
		return
	}
	var resolved int
	for _, route := range sample.Routes {
		agencyID, ok := routeAgencyIndex.Get(srv.ObaBaseURL, route.Id)
		if !ok {
			continue
		}
		resolved++
		if !agencyKeys[models.ServerKey(srv.ObaBaseURL, agencyID)] {
			t.Errorf("route %s resolved to agency %q, which has no stored bundle", route.Id, agencyID)
			break
		}
		if name, ok := routeAgencyIndex.AgencyNameFor(srv.ObaBaseURL, agencyID); !ok || name == "" {
			t.Errorf("agency %q has no name in the route index", agencyID)
			break
		}
	}
	if resolved == 0 {
		t.Errorf("route index for %s resolved none of %d routes; server-mode would attribute no vehicles to any agency",
			srv.ObaBaseURL, len(sample.Routes))
	}
}
