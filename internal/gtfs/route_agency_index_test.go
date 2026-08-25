package gtfs

import (
	"testing"
)

func TestRouteAgencyIndexSetAndGet(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server.example.com", map[string]string{
		"route-1": "agency-A",
		"route-2": "agency-B",
	})

	got, ok := idx.Get("https://server.example.com", "route-1")
	if !ok || got != "agency-A" {
		t.Fatalf("expected agency-A for route-1, got %q (ok=%v)", got, ok)
	}

	got, ok = idx.Get("https://server.example.com", "route-2")
	if !ok || got != "agency-B" {
		t.Fatalf("expected agency-B for route-2, got %q (ok=%v)", got, ok)
	}

	if _, ok := idx.Get("https://server.example.com", "route-missing"); ok {
		t.Fatalf("expected missing route to return ok=false")
	}
}

func TestRouteAgencyIndexEmptyRouteID(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server.example.com", map[string]string{"route-1": "agency-A"})
	if _, ok := idx.Get("https://server.example.com", ""); ok {
		t.Fatal("expected empty route_id to return ok=false")
	}
}

func TestRouteAgencyIndexServerIsolation(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server-a.example.com", map[string]string{"route-1": "agency-A"})
	idx.Set("https://server-b.example.com", map[string]string{"route-1": "agency-B"})

	gotA, _ := idx.Get("https://server-a.example.com", "route-1")
	gotB, _ := idx.Get("https://server-b.example.com", "route-1")
	if gotA != "agency-A" || gotB != "agency-B" {
		t.Fatalf("servers should be isolated; got %q / %q", gotA, gotB)
	}
}

func TestRouteAgencyIndexSetAgencyName(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server.example.com", map[string]string{"route-1": "agency-A"})
	idx.SetAgencyName("https://server.example.com", "agency-A", "Agency Alpha")

	name, ok := idx.AgencyNameFor("https://server.example.com", "agency-A")
	if !ok || name != "Agency Alpha" {
		t.Fatalf("expected 'Agency Alpha', got %q (ok=%v)", name, ok)
	}

	if _, ok := idx.AgencyNameFor("https://server.example.com", "agency-unknown"); ok {
		t.Fatal("expected unknown agency_id to return ok=false")
	}
}

func TestRouteAgencyIndexReplaceMap(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server.example.com", map[string]string{"route-1": "agency-A", "route-2": "agency-B"})
	// Replace with a smaller map (a 24h refresh shrinking the route set).
	idx.Set("https://server.example.com", map[string]string{"route-3": "agency-C"})

	if _, ok := idx.Get("https://server.example.com", "route-1"); ok {
		t.Fatal("expected route-1 to be gone after replace")
	}
	got, ok := idx.Get("https://server.example.com", "route-3")
	if !ok || got != "agency-C" {
		t.Fatalf("expected route-3 -> agency-C after replace, got %q (ok=%v)", got, ok)
	}
}

func TestRouteAgencyIndexClear(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://server.example.com", map[string]string{"route-1": "agency-A"})
	idx.Clear("https://server.example.com")
	if _, ok := idx.Get("https://server.example.com", "route-1"); ok {
		t.Fatal("expected route to be gone after Clear")
	}
}

func TestRouteAgencyIndexRangeServerKeys(t *testing.T) {
	idx := NewRouteAgencyIndex()
	idx.Set("https://a.example.com", map[string]string{"r": "x"})
	idx.Set("https://b.example.com", map[string]string{"r": "y"})

	seen := make(map[string]bool)
	idx.RangeServerKeys(func(k string) bool {
		seen[k] = true
		return true
	})
	if !seen["https://a.example.com"] || !seen["https://b.example.com"] {
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
