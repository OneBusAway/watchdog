package gtfs

import (
	"reflect"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

func TestRouteAgencyIndexSetAndGet(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{
		"route-1": "agency-A",
		"route-2": "agency-B",
	}, nil, nil)

	got, ok := idx.Get(key, "route-1")
	if !ok || got != "agency-A" {
		t.Fatalf("expected agency-A for route-1, got %q (ok=%v)", got, ok)
	}

	got, ok = idx.Get(key, "route-2")
	if !ok || got != "agency-B" {
		t.Fatalf("expected agency-B for route-2, got %q (ok=%v)", got, ok)
	}

	if _, ok := idx.Get(key, "route-missing"); ok {
		t.Fatalf("expected missing route to return ok=false")
	}
}

func TestRouteAgencyIndexEmptyRouteID(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{"route-1": "agency-A"}, nil, nil)
	if _, ok := idx.Get(key, ""); ok {
		t.Fatal("expected empty route_id to return ok=false")
	}
}

func TestRouteAgencyIndexServerIsolation(t *testing.T) {
	idx := NewRouteAgencyIndex()
	keyA := models.ServerKey("https://server-a.example.com", "")
	keyB := models.ServerKey("https://server-b.example.com", "")
	idx.Replace(keyA, map[string]string{"route-1": "agency-A"}, nil, nil)
	idx.Replace(keyB, map[string]string{"route-1": "agency-B"}, nil, nil)

	gotA, _ := idx.Get(keyA, "route-1")
	gotB, _ := idx.Get(keyB, "route-1")
	if gotA != "agency-A" || gotB != "agency-B" {
		t.Fatalf("servers should be isolated; got %q / %q", gotA, gotB)
	}
}

func TestRouteAgencyIndexReplacePublishesAgencyName(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{"route-1": "agency-A"}, nil, map[string]string{"agency-A": "Agency Alpha"})

	name, ok := idx.AgencyNameFor(key, "agency-A")
	if !ok || name != "Agency Alpha" {
		t.Fatalf("expected 'Agency Alpha', got %q (ok=%v)", name, ok)
	}

	if _, ok := idx.AgencyNameFor(key, "agency-unknown"); ok {
		t.Fatal("expected unknown agency_id to return ok=false")
	}
}

func TestRouteAgencyIndexReplaceMap(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{"route-1": "agency-A", "route-2": "agency-B"}, nil, nil)
	// Replace with a smaller map (a 24h refresh shrinking the route set).
	idx.Replace(key, map[string]string{"route-3": "agency-C"}, nil, nil)

	if _, ok := idx.Get(key, "route-1"); ok {
		t.Fatal("expected route-1 to be gone after replace")
	}
	got, ok := idx.Get(key, "route-3")
	if !ok || got != "agency-C" {
		t.Fatalf("expected route-3 -> agency-C after replace, got %q (ok=%v)", got, ok)
	}
}

func TestRouteAgencyIndexClear(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{"route-1": "agency-A"}, nil, nil)
	idx.Clear(key)
	if _, ok := idx.Get(key, "route-1"); ok {
		t.Fatal("expected route to be gone after Clear")
	}
}

func TestRouteAgencyIndexRangeServerKeys(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Replace(models.ServerKey("https://a.example.com", ""), map[string]string{"r": "x"}, nil, nil)
	idx.Replace(models.ServerKey("https://b.example.com", ""), map[string]string{"r": "y"}, nil, nil)

	seen := make(map[string]bool)
	idx.RangeServerKeys(func(k string) bool {
		seen[k] = true
		return true
	})
	if !seen[models.ServerKey("https://a.example.com", "")] || !seen[models.ServerKey("https://b.example.com", "")] {
		t.Fatalf("RangeServerKeys did not return both keys: %+v", seen)
	}

	// Early-stop semantics: returning false halts iteration.
	count := 0
	idx.RangeServerKeys(func(string) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("expected iteration to stop after one call, got %d", count)
	}
}

func TestRouteAgencyIndexResolvesRouteAndTrip(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.Replace(key, map[string]string{"route-a": "A", "route-b": "B"}, map[string]string{"trip-a": "A", "trip-b": "B"}, map[string]string{"A": "Agency A"})

	for _, tc := range []struct {
		name, routeID, tripID, want string
		ok                          bool
	}{
		{"route only", "route-a", "", "A", true},
		{"trip only", "", "trip-a", "A", true},
		{"matching", "route-a", "trip-a", "A", true},
		{"conflict", "route-a", "trip-b", "", false},
		{"unknown", "missing", "missing", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := idx.ResolveVehicleAgency(key, tc.routeID, tc.tripID)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ResolveVehicleAgency() = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRouteAgencyIndexResolvesCandidateIntersections(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.ReplaceCandidates(key,
		map[string][]string{
			"route-ab": {"B", "A", "A"},
			"route-a":  {"A"},
		},
		map[string][]string{
			"trip-a":  {"A"},
			"trip-ab": {"A", "B"},
			"trip-bc": {"B", "C"},
			"trip-b":  {"B"},
		},
		map[string]string{"A": "Agency A", "B": "Agency B", "C": "Agency C"},
	)

	for _, tc := range []struct {
		name, routeID, tripID string
		wantAgency            string
		wantReason            AttributionFailureReason
		wantCandidates        []string
	}{
		{"unique intersection", "route-ab", "trip-a", "A", "", []string{"A"}},
		{"overlapping intersection", "route-ab", "trip-bc", "B", "", []string{"B"}},
		{"ambiguous intersection", "route-ab", "trip-ab", "", AttributionAmbiguousIdentifier, []string{"A", "B"}},
		{"ambiguous route", "route-ab", "", "", AttributionAmbiguousIdentifier, []string{"A", "B"}},
		{"route trip conflict", "route-a", "trip-b", "", AttributionRouteTripConflict, []string{"A", "B"}},
		{"trip fallback", "missing", "trip-a", "A", "", []string{"A"}},
		{"missing route", "", "", "", AttributionMissingRouteID, nil},
		{"unknown route", "missing", "", "", AttributionUnknownRouteID, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := idx.ResolveVehicleAttribution(key, tc.routeID, tc.tripID)
			if got.AgencyID != tc.wantAgency || got.Reason != tc.wantReason || !reflect.DeepEqual(got.Candidates, tc.wantCandidates) {
				t.Fatalf("ResolveVehicleAttribution() = %+v; want agency=%q reason=%q candidates=%v", got, tc.wantAgency, tc.wantReason, tc.wantCandidates)
			}
		})
	}

	if _, ok := idx.Get(key, "route-ab"); ok {
		t.Fatal("ambiguous route unexpectedly resolved through singleton compatibility lookup")
	}
	if got := idx.AgencyNames(key); !reflect.DeepEqual(got, map[string]string{"A": "Agency A", "B": "Agency B", "C": "Agency C"}) {
		t.Fatalf("agency names = %v", got)
	}
}

func TestRouteAgencyIndexAgencyKeysAreIsolated(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Replace(models.ServerKey("https://server.example.com", "A"), map[string]string{"route": "A"}, nil, nil)
	idx.Replace(models.ServerKey("https://server.example.com", "B"), map[string]string{"route": "B"}, nil, nil)
	if got, _ := idx.Get(models.ServerKey("https://server.example.com", "A"), "route"); got != "A" {
		t.Fatalf("agency A was overwritten: %q", got)
	}
	if got, _ := idx.Get(models.ServerKey("https://server.example.com", "B"), "route"); got != "B" {
		t.Fatalf("agency B was overwritten: %q", got)
	}
}

func TestRouteAgencyIndexAttributesOnlyTo(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "A")
	if idx.AttributesOnlyTo(key, "A") {
		t.Fatal("expected false without a snapshot")
	}
	idx.Replace(key, map[string]string{"route-a": "A"}, map[string]string{"trip-a": "A", "trip-x": ""}, nil)
	if !idx.AttributesOnlyTo(key, "A") {
		t.Fatal("expected true when every attribution names A")
	}
	if idx.AttributesOnlyTo(key, "B") {
		t.Fatal("expected false for an agency the snapshot does not name")
	}
	idx.Replace(key, map[string]string{"route-a": "A"}, map[string]string{"trip-b": "B"}, nil)
	if idx.AttributesOnlyTo(key, "A") {
		t.Fatal("expected false when a trip belongs to another agency")
	}
}

func TestRouteAgencyIndexSnapshotRemainsConsistentAcrossReplace(t *testing.T) {
	idx := NewRouteAgencyIndex()
	key := models.ServerKey("https://server.example.com", "")
	idx.ReplaceCandidates(key, map[string][]string{"route": {"A", "B"}}, map[string][]string{"trip": {"A"}}, map[string]string{"A": "Agency A", "B": "Agency B"})
	snapshot := idx.Snapshot(key)

	idx.ReplaceCandidates(key, map[string][]string{"route": {"C"}}, nil, map[string]string{"C": "Agency C"})

	if got := snapshot.ResolveVehicleAttribution(key, "route", "trip"); got.AgencyID != "A" || !got.Resolved() {
		t.Fatalf("snapshot changed after replace: %+v", got)
	}
	if got := snapshot.AgencyNames(key); !reflect.DeepEqual(got, map[string]string{"A": "Agency A", "B": "Agency B"}) {
		t.Fatalf("snapshot names changed after replace: %v", got)
	}
	if got := idx.ResolveVehicleAttribution(key, "route", ""); got.AgencyID != "C" || !got.Resolved() {
		t.Fatalf("live index did not replace snapshot: %+v", got)
	}
}
