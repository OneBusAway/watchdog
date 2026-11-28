package metrics

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

func TestCountVehiclePositionsUsesAgencyStore(t *testing.T) {
	serverA := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	serverB := models.ObaServer{AgencyID: "agency-b", ObaBaseURL: "https://b.example.com", AgencyName: "Agency B"}
	store := testRealtimeStore(t, serverA)
	count, err := countVehiclePositions(serverA, nil, store, nil)
	if err != nil || count == 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := countVehiclePositions(serverB, nil, store, nil); err == nil {
		t.Fatal("expected missing agency feed error")
	}
}

func TestCountVehiclePositionsKeepsDistinctServersWithSameAgencyIDDisjoint(t *testing.T) {
	// Two deployments share the same agency ID but run on different base URLs.
	// Their series must not collide: the server_url label keeps them distinct,
	// mirroring the composite ServerKey used by the stores.
	serverA := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	serverB := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://b.example.com", AgencyName: "Agency A Clone"}

	storeA := testRealtimeStore(t, serverA)
	storeB := testRealtimeStore(t, serverB)

	countA, err := countVehiclePositions(serverA, nil, storeA, nil)
	if err != nil || countA == 0 {
		t.Fatalf("server A: count=%d err=%v", countA, err)
	}
	countB, err := countVehiclePositions(serverB, nil, storeB, nil)
	if err != nil || countB == 0 {
		t.Fatalf("server B: count=%d err=%v", countB, err)
	}
	if countA != countB {
		t.Fatalf("fixture feeds should contain the same vehicle count, got %d vs %d", countA, countB)
	}

	metricA, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverA.AgencyID,
		"agency_name": serverA.AgencyName,
		"server_name": serverA.ServerName,
		"server_url":  "https://a.example.com",
	})
	if err != nil {
		t.Fatalf("failed to read series for %s: %v", serverA.ObaBaseURL, err)
	}
	metricB, err := getMetricValue(RealtimeVehiclePositions, map[string]string{
		"agency_id":   serverB.AgencyID,
		"agency_name": serverB.AgencyName,
		"server_name": serverB.ServerName,
		"server_url":  "https://b.example.com",
	})
	if err != nil {
		t.Fatalf("failed to read series for %s: %v", serverB.ObaBaseURL, err)
	}

	if metricA != float64(countA) {
		t.Fatalf("expected %s series to be %d, got %v", serverA.ObaBaseURL, countA, metricA)
	}
	if metricB != float64(countB) {
		t.Fatalf("expected %s series to be %d, got %v", serverB.ObaBaseURL, countB, metricB)
	}
}

func TestCountActiveVehiclesForAgency(t *testing.T) {
	ts := setupObaServer(t, `{"data":{"list":[{"vehicleId":"1"},{"vehicleId":"2"}]}}`, http.StatusOK)
	defer ts.Close()
	server := models.ObaServer{AgencyID: "agency-a", AgencyName: "Agency A", ServerName: "test-server", ObaBaseURL: ts.URL, ObaApiKey: "key"}
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)
	count, err := countActiveVehiclesForAgency(context.Background(), client, server)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if got := seriesMatching(ObaVehiclesLastSuccessfulFetch, map[string]string{
		"agency_id": "agency-a", "agency_name": "Agency A", "server_name": "test-server",
		"server_url": utils.SanitizeServerURL(ts.URL),
	}); len(got) != 1 {
		t.Fatalf("expected one vehicles freshness series, got %d", len(got))
	}
}

func TestCountActiveVehiclesForAgencyDeletesGaugeOnFetchFailure(t *testing.T) {
	ts := setupObaServer(t, `{"error":"internal server error"}`, http.StatusInternalServerError)
	defer ts.Close()

	server := models.ObaServer{
		AgencyID:   "agency-failure-test",
		AgencyName: "Agency Failure Test",
		ServerName: "test-server",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "key",
	}

	labels := []string{
		server.AgencyID,
		server.AgencyName,
		server.ServerName,
		utils.SanitizeServerURL(server.ObaBaseURL),
	}
	labelFilter := map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
		"server_url":  utils.SanitizeServerURL(server.ObaBaseURL),
	}

	// Seed a stale, nonzero value as if an earlier fetch had succeeded.
	AgencyActiveVehiclesGauge.WithLabelValues(labels...).Set(42)

	if got := seriesMatching(AgencyActiveVehiclesGauge, labelFilter); len(got) != 1 {
		t.Fatalf("expected seeded gauge series, got %d", len(got))
	}

	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	_, err := countActiveVehiclesForAgency(context.Background(), client, server)
	if err == nil {
		t.Fatal("expected error from failed OBA fetch")
	}

	if got := seriesMatching(AgencyActiveVehiclesGauge, labelFilter); len(got) != 0 {
		t.Fatalf("expected active-vehicle gauge series to be deleted after fetch failure, got %d series", len(got))
	}
}

// TestCountActiveVehiclesForAgencyKeepsGaugeWhenCanceled verifies that a call
// canceled by the cycle deadline or shutdown leaves the previous
// oba_agency_active_vehicles value in place, since cancellation means an
// incomplete collection rather than an outage.
func TestCountActiveVehiclesForAgencyKeepsGaugeWhenCanceled(t *testing.T) {
	ts := setupObaServer(t, `{"data":{"list":[{"vehicleId":"1"}]}}`, http.StatusOK)
	defer ts.Close()

	server := models.ObaServer{
		AgencyID:   "agency-canceled-test",
		AgencyName: "Agency Canceled Test",
		ServerName: "test-server",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "key",
	}
	labels := []string{server.AgencyID, server.AgencyName, server.ServerName, utils.SanitizeServerURL(server.ObaBaseURL)}
	labelFilter := map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
		"server_url":  utils.SanitizeServerURL(server.ObaBaseURL),
	}

	AgencyActiveVehiclesGauge.WithLabelValues(labels...).Set(42)

	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := countActiveVehiclesForAgency(ctx, client, server); err == nil {
		t.Fatal("expected error from canceled call")
	}

	got := seriesMatching(AgencyActiveVehiclesGauge, labelFilter)
	if len(got) != 1 {
		t.Fatalf("expected active-vehicle gauge series to survive cancellation, got %d series", len(got))
	}
	if v := got[0].GetGauge().GetValue(); v != 42 {
		t.Fatalf("expected previous value 42 to be kept, got %v", v)
	}
}

func TestCountActiveVehiclesForAgencyDeletesGaugeOnNilResponse(t *testing.T) {
	ts := setupObaServer(t, `null`, http.StatusOK)
	defer ts.Close()

	server := models.ObaServer{
		AgencyID:   "agency-nil-test",
		AgencyName: "Agency Nil Test",
		ServerName: "test-server",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "key",
	}

	labels := []string{
		server.AgencyID,
		server.AgencyName,
		server.ServerName,
		utils.SanitizeServerURL(server.ObaBaseURL),
	}

	labelFilter := map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
		"server_url":  utils.SanitizeServerURL(server.ObaBaseURL),
	}

	// Seed a stale, nonzero value as if an earlier fetch had succeeded.
	AgencyActiveVehiclesGauge.WithLabelValues(labels...).Set(42)

	if got := seriesMatching(AgencyActiveVehiclesGauge, labelFilter); len(got) != 1 {
		t.Fatalf("expected seeded gauge series, got %d", len(got))
	}

	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	count, err := countActiveVehiclesForAgency(context.Background(), client, server)
	if err == nil {
		t.Fatal("expected error for nil API response")
	}

	if count != 0 {
		t.Fatalf("expected vehicle count 0, got %d", count)
	}

	if got := seriesMatching(AgencyActiveVehiclesGauge, labelFilter); len(got) != 0 {
		t.Fatalf("expected active-vehicle gauge series to be deleted after nil response, got %d series", len(got))
	}
}

func TestTrackInvalidVehiclesUsesAgencyBounds(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}
	missing := models.ObaServer{AgencyID: "missing", ObaBaseURL: "https://missing.example.com", AgencyName: "Missing"}
	store := testRealtimeStore(t, server)
	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, nil, bounds, store, nil); err != nil {
		t.Fatal(err)
	}
	if err := trackInvalidVehiclesAndStoppedOutOfBounds(missing, nil, bounds, store, nil); err == nil {
		t.Fatal("expected missing feed error")
	}
}

func TestTrackVehicleTelemetrySeparatesSameIDAcrossFeeds(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A"}

	now := time.Now().UTC()
	vehicleA := remoteGtfs.Vehicle{
		ID:        &remoteGtfs.VehicleID{ID: "101"},
		Position:  &remoteGtfs.Position{Latitude: float32Ptr(47.60), Longitude: float32Ptr(-122.30)},
		Timestamp: &now,
	}
	vehicleB := remoteGtfs.Vehicle{
		ID:        &remoteGtfs.VehicleID{ID: "101"},
		Position:  &remoteGtfs.Position{Latitude: float32Ptr(47.65), Longitude: float32Ptr(-122.35)},
		Timestamp: &now,
	}

	data := &models.RealtimeData{Vehicles: []models.RealtimeVehicle{
		{Vehicle: vehicleA, FeedID: "0"},
		{Vehicle: vehicleB, FeedID: "1"},
	}}
	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), data)

	lastSeen := NewVehicleLastSeen()
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Same vehicle ID from different feeds must not share a last-seen slot:
	// they are two distinct physical vehicles.
	lastSeen0, ok0 := lastSeen.Get(server.ServerKey(), "0", "101")
	lastSeen1, ok1 := lastSeen.Get(server.ServerKey(), "1", "101")
	if !ok0 || !ok1 {
		t.Fatalf("expected both feeds' vehicle 101 to be tracked, got feed0=%v feed1=%v", ok0, ok1)
	}
	if got := lastSeen.Count(server.ServerKey()); got != 2 {
		t.Fatalf("expected 2 tracked vehicles (same ID, distinct feeds), got %d", got)
	}
	if lastSeen0.Lat == lastSeen1.Lat {
		t.Fatalf("expected separate last-seen slots per feed, got %+v and %+v", lastSeen0, lastSeen1)
	}
}

func float32Ptr(v float32) *float32 {
	return &v
}

func TestCountActiveVehiclesForAgencyRedactsAPIKeyFromErrors(t *testing.T) {
	rec := report.CaptureSentry(t)
	ts := setupObaServer(t, `{"error":"not found"}`, http.StatusNotFound)
	defer ts.Close()

	server := models.ObaServer{
		AgencyID:   "agency-redaction-test",
		AgencyName: "Agency Redaction Test",
		ServerName: "redaction-test",
		ObaBaseURL: ts.URL,
		ObaApiKey:  "super-secret-api-key",
	}
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
		option.WithMaxRetries(0),
	)

	_, err := countActiveVehiclesForAgency(context.Background(), client, server)
	if err == nil {
		t.Fatal("expected the upstream API request to fail")
	}
	if strings.Contains(err.Error(), server.ObaApiKey) || strings.Contains(err.Error(), "?key=") {
		t.Errorf("returned error exposed the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "/api/where/vehicles-for-agency/") {
		t.Errorf("expected the sanitized error to retain the API endpoint: %v", err)
	}

	for _, event := range rec.Events() {
		for _, exception := range event.Exception {
			if strings.Contains(exception.Value, server.ObaApiKey) || strings.Contains(exception.Value, "?key=") {
				t.Errorf("Sentry exception exposed the API key: %s", exception.Value)
			}
		}
	}
}

func TestValidateVehicleTimestamp(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		ts        *time.Time
		now       time.Time
		wantValid bool
	}{
		{
			name:      "nil timestamp is treated as missing, not malformed",
			ts:        nil,
			now:       now,
			wantValid: true,
		},
		{
			name:      "current timestamp is valid",
			ts:        &now,
			now:       now,
			wantValid: true,
		},
		{
			name: "recent past timestamp is valid",
			ts: func() *time.Time {
				t := now.Add(-5 * time.Minute)
				return &t
			}(),
			now:       now,
			wantValid: true,
		},
		{
			name: "slight future timestamp within skew tolerance is valid",
			ts: func() *time.Time {
				t := now.Add(2 * time.Minute)
				return &t
			}(),
			now:       now,
			wantValid: true,
		},
		{
			name:      "zero time value is invalid epoch_zero",
			ts:        &time.Time{},
			now:       now,
			wantValid: false,
		},
		{
			name: "unix epoch zero is invalid epoch_zero",
			ts: func() *time.Time {
				t := time.Unix(0, 0).UTC()
				return &t
			}(),
			now:       now,
			wantValid: false,
		},
		{
			name: "negative unix timestamp is invalid epoch_zero",
			ts: func() *time.Time {
				t := time.Unix(-100, 0).UTC()
				return &t
			}(),
			now:       now,
			wantValid: false,
		},
		{
			name: "millisecond timestamp is invalid milliseconds",
			ts: func() *time.Time {
				t := time.Unix(1794592160000, 0).UTC()
				return &t
			}(),
			now:       now,
			wantValid: false,
		},
		{
			name: "extreme future clock skew is invalid future",
			ts: func() *time.Time {
				t := now.Add(15 * time.Minute)
				return &t
			}(),
			now:       now,
			wantValid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid := validateVehicleTimestamp(tc.ts, tc.now)
			if valid != tc.wantValid {
				t.Errorf("validateVehicleTimestamp() = %v, want %v", valid, tc.wantValid)
			}
		})
	}
}

func TestTrackInvalidVehicleTimestampsAgencyMode(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A", ServerName: "server-a"}
	now := time.Now().UTC()

	store := gtfs.NewRealtimeStore()
	lat := float32(47.6)
	lon := float32(-122.3)

	validTs := now.Add(-1 * time.Minute)
	msTs := time.Unix(1794592160000, 0).UTC()
	futureTs := now.Add(2 * time.Hour)
	zeroTs := time.Unix(0, 0).UTC()

	vehicles := []models.RealtimeVehicle{
		{Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v-valid"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &validTs}},
		{Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v-ms"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &msTs}},
		{Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v-future"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &futureTs}},
		{Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v-zero"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: &zeroTs}},
		{Vehicle: remoteGtfs.Vehicle{ID: &remoteGtfs.VehicleID{ID: "v-nil"}, Position: &remoteGtfs.Position{Latitude: &lat, Longitude: &lon}, Timestamp: nil}},
	}
	store.Set(server.ServerKey(), &models.RealtimeData{
		Vehicles:      vehicles,
		ObservationAt: now,
	})

	bounds := geo.NewBoundingBoxStore()
	bounds.Set(server.ServerKey(), geo.BoundingBox{MinLat: -90, MaxLat: 90, MinLon: -180, MaxLon: 180})

	if err := trackInvalidVehiclesAndStoppedOutOfBounds(server, nil, bounds, store, nil); err != nil {
		t.Fatalf("track: %v", err)
	}

	got, err := getMetricValue(InvalidVehicleTimestampsGauge, map[string]string{
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
		"server_url":  server.ObaBaseURL,
	})
	if err != nil {
		t.Fatalf("get metric: %v", err)
	}
	// Out of 5 vehicles: 1 valid, 1 nil (missing, not malformed), 3 invalid (ms, future, zero)
	if got != 3 {
		t.Fatalf("expected 3 invalid timestamps, got %v", got)
	}
}

func TestTrackVehicleTelemetryRejectsMalformedTimestamp(t *testing.T) {
	server := models.ObaServer{AgencyID: "agency-a", ObaBaseURL: "https://a.example.com", AgencyName: "Agency A", ServerName: "server-a"}
	now := time.Now().UTC()
	msTs := time.Unix(1794592160000, 0).UTC()
	lat := float32(47.6)
	lon := float32(-122.3)

	store := gtfs.NewRealtimeStore()
	store.Set(server.ServerKey(), &models.RealtimeData{
		Vehicles: []models.RealtimeVehicle{
			{FeedID: "0", StateHash: "h1", Vehicle: remoteGtfs.Vehicle{
				ID:        &remoteGtfs.VehicleID{ID: "v-ms"},
				Position:  &remoteGtfs.Position{Latitude: &lat, Longitude: &lon},
				Timestamp: &msTs,
			}},
		},
		ObservationAt: now,
	})

	lastSeen := NewVehicleLastSeen()
	if err := trackVehicleTelemetry(server, nil, lastSeen, store, gtfs.NewRouteAgencyIndex()); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Source timestamp should NOT be emitted for malformed timestamp (year 58838)
	labels := map[string]string{
		"vehicle_id":  "v-ms",
		"agency_id":   server.AgencyID,
		"agency_name": server.AgencyName,
		"server_name": server.ServerName,
		"server_url":  server.ObaBaseURL,
		"feed":        "0",
	}
	if series := seriesMatching(GtfsRtVehicleSourceTimestamp, labels); len(series) != 0 {
		t.Fatalf("expected no source timestamp emitted for millisecond timestamp, got %d series", len(series))
	}
	if series := seriesMatching(VehicleSpeedGauge, labels); len(series) != 0 {
		t.Fatalf("expected no speed emitted for millisecond timestamp, got %d series", len(series))
	}

	// LastSeen time should NOT be polluted with ms timestamp
	seen, ok := lastSeen.Get(server.ServerKey(), "0", "v-ms")
	if !ok {
		t.Fatal("expected vehicle to be tracked in LastSeen")
	}
	if !seen.Time.IsZero() {
		t.Fatalf("expected LastSeen.Time to be zero for invalid timestamp, got %v", seen.Time)
	}
}

// LastSeenKey must match the key trackVehicleTelemetry files a vehicle under.
func TestVehicleAttributorLastSeenKey(t *testing.T) {
	agencyMode := models.ObaServer{ObaBaseURL: "https://last-seen.example", AgencyID: "a"}
	if got := NewVehicleAttributor(agencyMode, nil, nil).LastSeenKey(agencyMode); got != agencyMode.ServerKey() {
		t.Errorf("agency mode key = %q, want %q", got, agencyMode.ServerKey())
	}

	serverMode := models.ObaServer{ObaBaseURL: "https://last-seen.example"}
	agencyB := models.ObaServer{ObaBaseURL: serverMode.ObaBaseURL, AgencyID: "b"}
	attributor := NewVehicleAttributor(serverMode, []models.ObaServer{agencyB}, nil)
	if got, want := attributor.LastSeenKey(agencyB), models.ServerKey(serverMode.ObaBaseURL, "b"); got != want {
		t.Errorf("attributed key = %q, want %q", got, want)
	}
	if got := attributor.LastSeenKey(models.ObaServer{}); got != serverMode.ServerKey() {
		t.Errorf("unattributed key = %q, want %q", got, serverMode.ServerKey())
	}
}
