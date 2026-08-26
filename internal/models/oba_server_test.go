package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestObaServerUnmarshalJSONStaticFeedsKey(t *testing.T) {
	var s ObaServer
	if err := json.Unmarshal([]byte(`{"agency_id":"a","gtfs_static_feeds":["https://a.example.com/gtfs.zip"]}`), &s); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !reflect.DeepEqual(s.GtfsStaticFeeds, []string{"https://a.example.com/gtfs.zip"}) {
		t.Errorf("expected canonical static feeds, got %+v", s.GtfsStaticFeeds)
	}
}

func TestNewObaServer(t *testing.T) {
	agencyName := "Test Server"
	agencyID := "test-agency-id"
	baseURL := "https://test.onebusaway.org"
	apiKey := "test-key"
	gtfsStaticFeeds := []string{"https://test.gtfs.url"}
	gtfsRTFeeds := []GtfsRTFeed{{
		TripUpdateURL:      "https://test.tripupdate.url",
		VehiclePositionURL: "https://test.vehicleposition.url",
		GtfsRTAPIKey:       "test-gtfs-rt-api-key",
		GtfsRTAPIValue:     "test-gtfs-rt-api-value",
		AgencyIDs:          []string{agencyID},
	}}

	server := NewObaServer(
		"Test Server",
		agencyName,
		agencyID,
		baseURL,
		apiKey,
		gtfsStaticFeeds,
		gtfsRTFeeds,
	)

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"AgencyName", server.AgencyName, agencyName},
		{"AgencyID", server.AgencyID, agencyID},
		{"BaseURL", server.ObaBaseURL, baseURL},
		{"ApiKey", server.ObaApiKey, apiKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("NewObaServer() %s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}

	if len(server.GtfsStaticFeeds) != 1 || server.GtfsStaticFeeds[0] != gtfsStaticFeeds[0] {
		t.Errorf("NewObaServer() GtfsStaticFeeds = %v, want %v", server.GtfsStaticFeeds, gtfsStaticFeeds)
	}
	if !reflect.DeepEqual(server.GtfsRTFeeds, gtfsRTFeeds) {
		t.Errorf("NewObaServer() GtfsRTFeeds = %v, want %v", server.GtfsRTFeeds, gtfsRTFeeds)
	}
}

func TestServerKey(t *testing.T) {
	// Two distinct deployments that reuse the same agency ID must not collide.
	first := ObaServer{AgencyID: "1", ObaBaseURL: "https://first.example.com"}
	second := ObaServer{AgencyID: "1", ObaBaseURL: "https://second.example.com"}

	if first.ServerKey() == second.ServerKey() {
		t.Fatalf("distinct deployments must have distinct server keys, got %q", first.ServerKey())
	}
	if first.ServerKey() != ServerKey(first.ObaBaseURL, first.AgencyID) {
		t.Fatalf("method and package function disagree for the same server")
	}

	// Exact duplicates (same base URL and agency ID) must produce one key.
	dup := ServerKey(first.ObaBaseURL, first.AgencyID)
	if !strings.Contains(dup, "first.example.com") || !strings.Contains(dup, "|1") {
		t.Fatalf("expected server key to embed sanitized base URL and agency ID, got %q", dup)
	}
}

// TestOwnsServerKeyFollowsScopeRules pins the ownership rule the prune path
// depends on: a server-scoped entry claims every agency discovered under its
// base URL, an agency-scoped entry claims only its own key, and the two do not
// bleed into each other when they share a base URL.
func TestOwnsServerKeyFollowsScopeRules(t *testing.T) {
	const baseURL = "https://oba.example.com"
	serverScoped := ObaServer{ServerName: "multi", ObaBaseURL: baseURL}
	agencyScoped := ObaServer{ServerName: "solo", ObaBaseURL: baseURL, AgencyID: "agency-a"}

	for _, tc := range []struct {
		name          string
		entry         ObaServer
		key           string
		wantOwnership bool
	}{
		{"server-scoped owns a discovered agency", serverScoped, ServerKey(baseURL, "agency-b"), true},
		{"server-scoped owns its own agency-less key", serverScoped, ServerKey(baseURL, ""), true},
		{"server-scoped disowns another deployment", serverScoped, ServerKey("https://other.example.com", "agency-a"), false},
		{"agency-scoped owns only its key", agencyScoped, ServerKey(baseURL, "agency-a"), true},
		{"agency-scoped disowns a sibling agency", agencyScoped, ServerKey(baseURL, "agency-b"), false},
	} {
		if got := tc.entry.OwnsServerKey(tc.key); got != tc.wantOwnership {
			t.Errorf("%s: OwnsServerKey(%q) = %v, want %v", tc.name, tc.key, got, tc.wantOwnership)
		}
	}
}

// TestParseServerKeyRoundTrips guards the composer/decomposer pair against
// drifting apart, and pins the difference between a server-scoped key (empty
// agency, ok) and a string that is not a server key at all (not ok).
func TestParseServerKeyRoundTrips(t *testing.T) {
	for _, agencyID := range []string{"agency-a", ""} {
		key := ServerKey("https://oba.example.com", agencyID)
		gotURL, gotAgency, ok := ParseServerKey(key)
		if !ok {
			t.Fatalf("ParseServerKey(%q) reported not-a-key", key)
		}
		if gotURL != "https://oba.example.com" || gotAgency != agencyID {
			t.Fatalf("ParseServerKey(%q) = (%q, %q), want (%q, %q)", key, gotURL, gotAgency, "https://oba.example.com", agencyID)
		}
	}
	if _, _, ok := ParseServerKey("no-separator-here"); ok {
		t.Fatal("expected a string with no separator to be reported as not a server key")
	}
}
