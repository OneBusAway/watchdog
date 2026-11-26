package gtfs

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
	// The binary embeds tzdata in main; tests embed it too so legacy links
	// like US/Pacific load on hosts without tzdata-legacy.
	_ "time/tzdata"

	"watchdog.onebusaway.org/internal/models"
)

func TestCompileSchedulesCalendarExceptionsAndOvernight(t *testing.T) {
	feed := makeScheduleZip(t, map[string]string{
		"agency.txt":         "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,UTC\n",
		"routes.txt":         "route_id,agency_id,route_short_name,route_type\nR,A,R,3\n",
		"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,0,0,0,0,0,0,20260901,20260930\n",
		"calendar_dates.txt": "service_id,date,exception_type\nWK,20260907,2\nWK,20260908,1\n",
		"trips.txt":          "route_id,service_id,trip_id\nR,WK,T\n",
		"stops.txt":          "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,1\nS2,Two,2,2\n",
		"stop_times.txt":     "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,25:00:00,25:00:00,S1,1\nT,26:00:00,26:00:00,S2,2\n",
	})
	server := scheduleServer("A", "https://a.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: feed})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)

	if available, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 8, 1, 30, 0, 0, time.UTC)); !available || active {
		t.Fatalf("removed Monday must suppress its overnight trip: available=%t active=%t", available, active)
	}
	if available, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 9, 1, 30, 0, 0, time.UTC)); !available || !active {
		t.Fatalf("added Tuesday must activate its overnight trip: available=%t active=%t", available, active)
	}
	snapshot := snapshots[server.ServerKey()]
	if snapshot.maxOvernightOffset != 26*time.Hour {
		t.Fatalf("max overnight offset = %s, want 26h", snapshot.maxOvernightOffset)
	}
}

func TestCompileSchedulesRealFixture(t *testing.T) {
	server := scheduleServer("40", "https://fixture.example/gtfs.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: readFixture(t, "gtfs.zip")})
	if err != nil {
		t.Fatalf("compile repository GTFS fixture: %v", err)
	}
	snapshot := snapshots[server.ServerKey()]
	if snapshot == nil || !snapshot.complete || snapshot.timezoneName != "America/Los_Angeles" || len(snapshot.services) == 0 {
		t.Fatalf("unexpected fixture snapshot: %+v", snapshot)
	}
}

func TestCompileSchedulesCalendarDatesOnly(t *testing.T) {
	feed := basicScheduleFiles("UTC", "12:00:00", "13:00:00")
	delete(feed, "calendar.txt")
	feed["calendar_dates.txt"] = "service_id,date,exception_type\nWK,20260910,1\n"
	server := scheduleServer("A", "https://dates-only.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)})
	if err != nil {
		t.Fatalf("compile calendar-dates-only feed: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if available, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 10, 12, 30, 0, 0, time.UTC)); !available || !active {
		t.Fatalf("calendar_dates addition should be active: available=%t active=%t", available, active)
	}
	if _, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 11, 12, 30, 0, 0, time.UTC)); active {
		t.Fatal("calendar-dates-only service must not infer adjacent service days")
	}
}

func TestCompileSchedulesCalendarDatesOnlyDateRangeSpansAllDates(t *testing.T) {
	feed := basicScheduleFiles("UTC", "12:00:00", "13:00:00")
	delete(feed, "calendar.txt")
	feed["calendar_dates.txt"] = "service_id,date,exception_type\nWK,20260910,1\nWK,20261231,1\nWK,20260905,1\n"
	server := scheduleServer("A", "https://dates-only-range.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)})
	if err != nil {
		t.Fatalf("compile calendar-dates-only feed: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	earliest, latest, ok := store.ServiceDateRange(server.ServerKey())
	want := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if !ok || !earliest.Equal(want) || !latest.Equal(want) {
		t.Fatalf("service date range = %s..%s (ok=%t), want end %s for both", earliest, latest, ok, want)
	}
}

func TestCompileSchedulesFrequencyWindows(t *testing.T) {
	files := basicScheduleFiles("UTC", "00:00:00", "00:10:00")
	files["frequencies.txt"] = "trip_id,start_time,end_time,headway_secs,exact_times\nT,06:00:00,07:00:00,1800,1\n"
	server := scheduleServer("A", "https://frequency.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile frequency feed: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	day := func(hour, minute int) time.Time { return time.Date(2026, 9, 7, hour, minute, 0, 0, time.UTC) }
	for _, tc := range []struct {
		at     time.Time
		active bool
	}{
		{day(6, 5), true},
		{day(6, 20), false},
		{day(6, 35), true},
		{day(6, 45), false},
		{day(7, 0), false},
	} {
		available, active := store.Evaluate(server.ServerKey(), tc.at)
		if !available || active != tc.active {
			t.Errorf("at %s: available=%t active=%t, want active=%t", tc.at, available, active, tc.active)
		}
	}
}

func TestCompileSchedulesApproximateFrequencyIsConservativelyActive(t *testing.T) {
	files := basicScheduleFiles("UTC", "00:00:00", "00:10:00")
	files["frequencies.txt"] = "trip_id,start_time,end_time,headway_secs,exact_times\nT,06:00:00,07:00:00,1800,0\n"
	server := scheduleServer("A", "https://approximate-frequency.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile approximate frequency feed: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if available, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 7, 6, 20, 0, 0, time.UTC)); !available || !active {
		t.Fatalf("inexact frequency gap must remain conservatively active: available=%t active=%t", available, active)
	}
}

func TestCompileSchedulesUsesAgencyOwnershipInServerMode(t *testing.T) {
	files := basicScheduleFiles("UTC", "10:00:00", "11:00:00")
	files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,UTC\nB,Agency B,https://b.example,UTC\n"
	files["routes.txt"] = "route_id,agency_id,route_short_name,route_type\nRA,A,A,3\nRB,B,B,3\n"
	files["trips.txt"] = "route_id,service_id,trip_id\nRA,WK,TA\nRB,WK,TB\n"
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nTA,10:00:00,10:00:00,S1,1\nTA,11:00:00,11:00:00,S2,2\nTB,10:00:00,10:00:00,S1,1\nTB,11:00:00,11:00:00,S2,2\n"
	server := scheduleServer("", "https://multi.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile server-mode feed: %v", err)
	}
	for _, agencyID := range []string{"A", "B"} {
		key := models.ServerKey(server.ObaBaseURL, agencyID)
		if snapshots[key] == nil || len(snapshots[key].services) != 1 {
			t.Errorf("agency %s did not receive its owned schedule: %+v", agencyID, snapshots[key])
		}
	}
}

func TestCompileSchedulesRejectsUnsafeParserFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"invalid timezone", func(files map[string]string) {
			files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,A,https://a.example,Mars/Olympus\n"
		}},
		{"mixed timezones", func(files map[string]string) {
			files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,A,https://a.example,UTC\nB,B,https://b.example,America/New_York\n"
		}},
		{"same offset today with different rules", func(files map[string]string) {
			files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,A,https://a.example,America/Phoenix\nB,B,https://b.example,America/Denver\n"
		}},
		{"neighboring zone with different historical rules", func(files map[string]string) {
			files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,A,https://a.example,America/Los_Angeles\nB,B,https://b.example,America/Tijuana\n"
		}},
		{"missing endpoint", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,2\n"
		}},
		{"invalid endpoint minute", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:99:00,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,2\n"
		}},
		{"unsupported extreme overnight", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,200:00:00,200:00:00,S2,2\n"
		}},
		{"empty stop_times", func(files map[string]string) {
			files["stop_times.txt"] = ""
		}},
		{"malformed stop_times row after valid rows", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,09:00:00,\"09:00:00,S2,2\n"
		}},
		{"stop_times references unknown trip", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nNOPE,09:00:00,09:00:00,S2,2\n"
		}},
		{"duplicate stop_sequence", func(files map[string]string) {
			files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:00:00,S1,1\nT,09:00:00,09:00:00,S2,1\n"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
			tc.mutate(files)
			server := scheduleServer("A", "https://invalid.zip")
			if _, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}); err == nil {
				t.Fatal("expected schedule compilation to fail")
			}
		})
	}
}

// San Diego's merged feed declares America/Los_Angeles for three agencies and
// the backward-compatibility link US/Pacific for the fourth. They are one zone,
// so the feed must compile, and each agency keeps the name it declared.
func TestCompileSchedulesAcceptsTimezoneAliases(t *testing.T) {
	files := basicScheduleFiles("UTC", "10:00:00", "11:00:00")
	files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,America/Los_Angeles\nB,Agency B,https://b.example,US/Pacific\n"
	files["routes.txt"] = "route_id,agency_id,route_short_name,route_type\nRA,A,A,3\nRB,B,B,3\n"
	files["trips.txt"] = "route_id,service_id,trip_id\nRA,WK,TA\nRB,WK,TB\n"
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nTA,10:00:00,10:00:00,S1,1\nTA,11:00:00,11:00:00,S2,2\nTB,10:00:00,10:00:00,S1,1\nTB,11:00:00,11:00:00,S2,2\n"
	server := scheduleServer("", "https://aliases.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile feed with aliased timezones: %v", err)
	}
	for agencyID, want := range map[string]string{"A": "America/Los_Angeles", "B": "US/Pacific"} {
		snapshot := snapshots[models.ServerKey(server.ObaBaseURL, agencyID)]
		if snapshot == nil || snapshot.timezoneName != want {
			t.Fatalf("agency %s snapshot = %+v, want timezone %q", agencyID, snapshot, want)
		}
	}
}

func TestSameTimezoneRules(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"America/Los_Angeles", "America/Los_Angeles", true},
		{"America/Los_Angeles", "US/Pacific", true},
		{"America/New_York", "US/Eastern", true},
		{"Pacific/Honolulu", "US/Hawaii", true},
		{"UTC", "Etc/UTC", true},
		{"Europe/Kyiv", "Europe/Kiev", true},
		{"UTC", "America/New_York", false},
		{"America/Phoenix", "America/Denver", false},
		{"America/Los_Angeles", "America/Tijuana", false},
		{"America/Los_Angeles", "America/Vancouver", false},
		{"America/Indiana/Indianapolis", "America/New_York", false},
	}
	for _, tc := range tests {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			a, b := mustLoadLocation(t, tc.a), mustLoadLocation(t, tc.b)
			if got := sameTimezoneRules(a, b); got != tc.want {
				t.Fatalf("sameTimezoneRules(%s, %s) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
			if got := sameTimezoneRules(b, a); got != tc.want {
				t.Fatalf("sameTimezoneRules(%s, %s) = %t, want %t", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// Two zones that change offset on the same day but an hour apart agree at
// every daily sample, so only the transition-instant comparison tells them
// apart.
func TestSameTimezoneRulesComparesTransitionInstants(t *testing.T) {
	transition := time.Date(2000, 4, 2, 10, 0, 0, 0, time.UTC)
	ten := syntheticZone(t, "Test/Ten", transition)
	if !sameTimezoneRules(ten, syntheticZone(t, "Test/TenAgain", transition)) {
		t.Fatal("zones with identical transitions must compare equal")
	}
	if sameTimezoneRules(ten, syntheticZone(t, "Test/Eleven", transition.Add(time.Hour))) {
		t.Fatal("zones whose transitions differ by an hour must not compare equal")
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return location
}

// syntheticZone builds a TZif v1 zone that is UTC-8 until transition and
// UTC-7 afterward.
func syntheticZone(t *testing.T, name string, transition time.Time) *time.Location {
	t.Helper()
	var data bytes.Buffer
	data.WriteString("TZif")
	data.Write(make([]byte, 16))
	// Counts: UT/local indicators, standard/wall indicators, leap seconds,
	// transitions, local time types, and abbreviation bytes.
	for _, count := range []uint32{0, 0, 0, 1, 2, 8} {
		_ = binary.Write(&data, binary.BigEndian, count)
	}
	_ = binary.Write(&data, binary.BigEndian, int32(transition.Unix()))
	data.WriteByte(1)
	_ = binary.Write(&data, binary.BigEndian, int32(-8*3600))
	data.Write([]byte{0, 0})
	_ = binary.Write(&data, binary.BigEndian, int32(-7*3600))
	data.Write([]byte{1, 4})
	data.WriteString("PST\x00PDT\x00")
	location, err := time.LoadLocationFromTZData(name, data.Bytes())
	if err != nil {
		t.Fatalf("build synthetic zone %s: %v", name, err)
	}
	return location
}

func TestCompileSchedulesMergedFeedAndAtomicAvailability(t *testing.T) {
	server := scheduleServer("A", "https://merged.zip")
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["calendar.txt"] += "PM,1,1,1,1,1,1,1,20260901,20260930\n"
	files["trips.txt"] += "R,PM,PM-T\n"
	files["stop_times.txt"] += "PM-T,17:00:00,17:00:00,S1,1\nPM-T,18:00:00,18:00:00,S2,2\n"
	feed := downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}
	snapshots, err := compileSchedules(server, feed)
	if err != nil {
		t.Fatalf("compile merged feed: %v", err)
	}
	snapshot := snapshots[server.ServerKey()]
	if len(snapshot.feedURLs) != 1 || len(snapshot.services) != 2 {
		t.Fatalf("provenance/services not retained: %+v", snapshot)
	}

	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if available, _ := store.Evaluate(server.ServerKey(), time.Now()); !available {
		t.Fatal("complete replacement should be available")
	}
	store.MarkUnavailable(server)
	if available, active := store.Evaluate(server.ServerKey(), time.Date(2026, 9, 7, 8, 30, 0, 0, time.UTC)); available || active {
		t.Fatalf("retained snapshot must not assert state after failure: available=%t active=%t", available, active)
	}
	if store.data[server.ServerKey()].snapshot != snapshot {
		t.Fatal("transient failure should preserve the last good snapshot")
	}
	removed := store.Prune(func(string) bool { return false })
	if len(removed) != 1 || len(store.data) != 0 {
		t.Fatalf("prune did not remove schedule state: removed=%v data=%v", removed, store.data)
	}
}

func TestScheduleEvaluationAcrossDSTUsesAgencyTimezone(t *testing.T) {
	files := basicScheduleFiles("America/New_York", "01:30:00", "03:30:00")
	files["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,0,0,0,0,0,0,1,20260308,20260308\n"
	server := scheduleServer("A", "https://dst.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile DST schedule: %v", err)
	}
	snapshot := snapshots[server.ServerKey()]
	day := time.Date(2026, 3, 8, 12, 0, 0, 0, snapshot.timezone)
	start := serviceTime(day, snapshot.timezone, 90*time.Minute)
	end := serviceTime(day, snapshot.timezone, 210*time.Minute)
	if !snapshot.activeAt(start.Add(30*time.Minute)) || snapshot.activeAt(end.Add(time.Second)) {
		t.Fatalf("DST window evaluation failed: start=%s end=%s", start, end)
	}
	if end.Sub(start) != 2*time.Hour {
		t.Fatalf("GTFS noon-based offsets should remain monotonic across DST, got %s", end.Sub(start))
	}
}

func scheduleServer(agencyID string, feeds ...string) models.ObaServer {
	return models.ObaServer{ServerName: "test", AgencyID: agencyID, AgencyName: "Agency A", ObaBaseURL: "https://oba.example", GtfsStaticFeeds: feeds}
}

func basicScheduleFiles(timezone, first, last string) map[string]string {
	return map[string]string{
		"agency.txt":     fmt.Sprintf("agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,%s\n", timezone),
		"routes.txt":     "route_id,agency_id,route_short_name,route_type\nR,A,R,3\n",
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,1,1,20260101,20261231\n",
		"trips.txt":      "route_id,service_id,trip_id\nR,WK,T\n",
		"stops.txt":      "stop_id,stop_name,stop_lat,stop_lon\nS1,One,1,1\nS2,Two,2,2\n",
		"stop_times.txt": fmt.Sprintf("trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,%s,%s,S1,1\nT,%s,%s,S2,2\n", first, first, last, last),
	}
}

func makeScheduleZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	for name, content := range files {
		file, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close schedule zip: %v", err)
	}
	return data.Bytes()
}

// classifyStore compiles files as one agency-mode feed for agency A and
// returns the store and the agency's server key.
func classifyStore(t *testing.T, files map[string]string) (*ScheduleStore, string) {
	t.Helper()
	server := scheduleServer("A", "https://classify.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	return store, server.ServerKey()
}

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func assertClassification(t *testing.T, got TripClassification, state ExpectedScheduleState, reason ExpectedScheduleReason, start, end time.Time) {
	t.Helper()
	if !got.ScheduleAvailable || got.State != state || got.Reason != reason {
		t.Fatalf("classification = %+v, want available %s/%s", got, state, reason)
	}
	if !got.WindowStart.Equal(start) || !got.WindowEnd.Equal(end) {
		t.Fatalf("window = %s..%s, want %s..%s", got.WindowStart, got.WindowEnd, start, end)
	}
	if !start.IsZero() && (got.WindowStart.Location() != time.UTC || got.WindowEnd.Location() != time.UTC) {
		t.Fatalf("window must be in UTC, got %s..%s", got.WindowStart.Location(), got.WindowEnd.Location())
	}
}

func TestClassifyTrip_NoSchedule(t *testing.T) {
	if got := NewScheduleStore().ClassifyTrip("https://oba.example|A", TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 30)); got != (TripClassification{}) {
		t.Fatalf("missing schedule = %+v, want every field empty", got)
	}

	// After a failed refresh the kept snapshot may be outdated, so it must not
	// classify trips.
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	store.MarkUnavailable(scheduleServer("A", "https://classify.zip"))
	if got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 30)); got != (TripClassification{}) {
		t.Fatalf("unavailable schedule = %+v, want every field empty", got)
	}
}

func TestClassifyTrip_NoTripDescriptor(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	got := store.ClassifyTrip(key, TripDescriptor{}, utc(2026, 9, 9, 8, 30))
	assertClassification(t, got, ExpectedScheduleUnknown, ExpectedReasonNoTripDescriptor, time.Time{}, time.Time{})
}

func TestClassifyTrip_TripNotInStatic(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "NOPE"}, utc(2026, 9, 9, 8, 30))
	assertClassification(t, got, ExpectedScheduleUnknown, ExpectedReasonTripNotInStaticGTFS, time.Time{}, time.Time{})
}

func TestClassifyTrip_FrequencyWithoutStartTime(t *testing.T) {
	files := basicScheduleFiles("UTC", "00:00:00", "00:10:00")
	files["frequencies.txt"] = "trip_id,start_time,end_time,headway_secs,exact_times\nT,06:00:00,07:00:00,1800,1\n"
	store, key := classifyStore(t, files)
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 6, 5))
	assertClassification(t, got, ExpectedScheduleUnknown, ExpectedReasonTripInstanceAmbiguous, time.Time{}, time.Time{})
}

// A start date or time that is present but can't be parsed doesn't identify a
// trip instance, so it is ambiguous rather than ignored.
func TestClassifyTrip_MalformedStartFields(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	for _, descriptor := range []TripDescriptor{
		{TripID: "T", StartDate: "2026-09-09"},
		{TripID: "T", StartTime: "8:00:00"},
		{TripID: "T", StartTime: "08:75:00"},
	} {
		got := store.ClassifyTrip(key, descriptor, utc(2026, 9, 9, 8, 30))
		assertClassification(t, got, ExpectedScheduleUnknown, ExpectedReasonTripInstanceAmbiguous, time.Time{}, time.Time{})
	}
}

func TestClassifyTrip_ServiceNotActive(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,0,0,0,0,0,0,20260101,20261231\n"
	store, key := classifyStore(t, files)
	// Wednesday; the candidates are Wednesday and Tuesday, and the trip runs
	// only on Mondays.
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 30))
	assertClassification(t, got, ExpectedScheduleInactive, ExpectedReasonServiceNotActiveOnDate, time.Time{}, time.Time{})
}

func TestClassifyTrip_WithinWindow(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 30))
	assertClassification(t, got, ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 9, 9, 8, 0), utc(2026, 9, 9, 9, 0))
}

func TestClassifyTrip_BeforeWindow(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	// Yesterday's window has ended and today's has not started; the earliest
	// window starting after T wins.
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 7, 0))
	assertClassification(t, got, ExpectedScheduleInactive, ExpectedReasonBeforeScheduledWindow, utc(2026, 9, 9, 8, 0), utc(2026, 9, 9, 9, 0))
}

func TestClassifyTrip_AfterWindow(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	// Both candidate windows have ended; the latest one is reported.
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 10, 0))
	assertClassification(t, got, ExpectedScheduleInactive, ExpectedReasonAfterScheduledWindow, utc(2026, 9, 9, 8, 0), utc(2026, 9, 9, 9, 0))
}

func TestClassifyTrip_StartDateGiven(t *testing.T) {
	store, key := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	// Without a start date, today's window would contain T. The given date
	// is the only candidate, so its window is reported as ended.
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T", StartDate: "20260908"}, utc(2026, 9, 9, 8, 30))
	assertClassification(t, got, ExpectedScheduleInactive, ExpectedReasonAfterScheduledWindow, utc(2026, 9, 8, 8, 0), utc(2026, 9, 8, 9, 0))
}

func TestClassifyTrip_OvernightTrip(t *testing.T) {
	files := basicScheduleFiles("UTC", "24:00:00", "25:30:00")
	// Tuesday 2026-09-08 only, so the 01:00 Wednesday observation can match
	// only the previous service date.
	files["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,0,1,0,0,0,0,0,20260908,20260908\n"
	store, key := classifyStore(t, files)
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 1, 0))
	assertClassification(t, got, ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 9, 9, 0, 0), utc(2026, 9, 9, 1, 30))
}

func TestClassifyTrip_DSTDay(t *testing.T) {
	files := basicScheduleFiles("America/New_York", "01:30:00", "03:30:00")
	files["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,0,0,0,0,0,0,1,20260308,20260308\n"
	store, key := classifyStore(t, files)
	// On 2026-03-08 noon is 16:00 UTC (EDT), so the reference is 04:00 UTC and
	// the 01:30 offset is 05:30 UTC, not the 06:30 UTC a midnight reference
	// would give.
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 3, 8, 6, 0))
	assertClassification(t, got, ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 3, 8, 5, 30), utc(2026, 3, 8, 7, 30))
}

func TestClassifyTrip_MidnightDSTTransition(t *testing.T) {
	// America/Havana transitions 00:00 -> 01:00 on 2026-03-08 (Sunday); local
	// midnight does not exist and shifts back to 23:00 on 2026-03-07 if used.
	havana := mustLoadLocation(t, "America/Havana")
	at := time.Date(2026, 3, 8, 8, 30, 0, 0, havana).UTC()
	wantStart := time.Date(2026, 3, 8, 12, 0, 0, 0, havana).Add(-4 * time.Hour).UTC() // 08:00 service offset
	wantEnd := time.Date(2026, 3, 8, 12, 0, 0, 0, havana).Add(-3 * time.Hour).UTC()   // 09:00 service offset

	dailyStore, dailyKey := classifyStore(t, basicScheduleFiles("America/Havana", "08:00:00", "09:00:00"))
	assertClassification(t, dailyStore.ClassifyTrip(dailyKey, TripDescriptor{TripID: "T"}, at),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, wantStart, wantEnd)
	assertClassification(t, dailyStore.ClassifyTrip(dailyKey, TripDescriptor{TripID: "T", StartDate: "20260308"}, at),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, wantStart, wantEnd)

	sundayFiles := basicScheduleFiles("America/Havana", "08:00:00", "09:00:00")
	sundayFiles["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,0,0,0,0,0,0,1,20260308,20260308\n"
	sundayStore, sundayKey := classifyStore(t, sundayFiles)
	assertClassification(t, sundayStore.ClassifyTrip(sundayKey, TripDescriptor{TripID: "T"}, at),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, wantStart, wantEnd)
}

func TestClassifyTrip_SpringForwardPreviousEveningServiceDay(t *testing.T) {
	// On 2026-03-08 (America/New_York spring-forward), noon is 16:00 UTC so the
	// Sunday service day starts at 04:00 UTC (2026-03-07 23:00 EST). An early
	// 00:15-00:45 Sunday trip runs 04:15-04:45 UTC on Saturday evening local time.
	at := utc(2026, 3, 8, 4, 30)
	wantStart, wantEnd := utc(2026, 3, 8, 4, 15), utc(2026, 3, 8, 4, 45)

	sundayFiles := basicScheduleFiles("America/New_York", "00:15:00", "00:45:00")
	sundayFiles["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,0,0,0,0,0,0,1,20260308,20260308\n"
	sundayStore, sundayKey := classifyStore(t, sundayFiles)
	assertClassification(t, sundayStore.ClassifyTrip(sundayKey, TripDescriptor{TripID: "T"}, at),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, wantStart, wantEnd)

	dailyStore, dailyKey := classifyStore(t, basicScheduleFiles("America/New_York", "00:15:00", "00:45:00"))
	assertClassification(t, dailyStore.ClassifyTrip(dailyKey, TripDescriptor{TripID: "T"}, at),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, wantStart, wantEnd)

	// Exact boundary: reference instant equals observation time (04:00 UTC).
	boundaryStore, boundaryKey := classifyStore(t, func() map[string]string {
		f := basicScheduleFiles("America/New_York", "00:00:00", "00:30:00")
		f["calendar.txt"] = sundayFiles["calendar.txt"]
		return f
	}())
	assertClassification(t, boundaryStore.ClassifyTrip(boundaryKey, TripDescriptor{TripID: "T"}, utc(2026, 3, 8, 4, 0)),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 3, 8, 4, 0), utc(2026, 3, 8, 4, 30))
}

func TestClassifyTrip_NegativeDurationClampedToZero(t *testing.T) {
	// Multi-stop trip whose last arrival (08:03) precedes first departure
	// (08:05) while overall span (08:00..08:10) remains valid.
	files := basicScheduleFiles("UTC", "08:00:00", "08:10:00")
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nT,08:00:00,08:05:00,S1,1\nT,08:03:00,08:10:00,S2,2\n"
	store, key := classifyStore(t, files)
	assertClassification(t, store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 5)),
		ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 9, 9, 8, 5), utc(2026, 9, 9, 8, 5))
	assertClassification(t, store.ClassifyTrip(key, TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 6)),
		ExpectedScheduleInactive, ExpectedReasonAfterScheduledWindow, utc(2026, 9, 9, 8, 5), utc(2026, 9, 9, 8, 5))
}

func TestClassifyTrip_FrequencyWithStartTime(t *testing.T) {
	files := basicScheduleFiles("UTC", "00:00:00", "00:10:00")
	files["frequencies.txt"] = "trip_id,start_time,end_time,headway_secs,exact_times\nT,06:00:00,07:00:00,1800,1\n"
	store, key := classifyStore(t, files)
	got := store.ClassifyTrip(key, TripDescriptor{TripID: "T", StartTime: "06:30:00"}, utc(2026, 9, 9, 6, 35))
	assertClassification(t, got, ExpectedScheduleActive, ExpectedReasonWithinScheduledWindow, utc(2026, 9, 9, 6, 30), utc(2026, 9, 9, 6, 40))
}

func TestLookupTrip(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["trips.txt"] = "route_id,service_id,trip_id,block_id\nR,WK,T,B1\nR,WK,T2,\n"
	files["stop_times.txt"] += "T2,10:00:00,10:00:00,S1,1\nT2,11:00:00,11:00:00,S2,2\n"
	store, key := classifyStore(t, files)

	wantT := TripLookup{ScheduleAvailable: true, Found: true, Trip: TripInfo{RouteID: "R", ServiceID: "WK", BlockID: "B1"}}
	if got := store.LookupTrip(key, "T"); got != wantT {
		t.Fatalf("LookupTrip(T) = %+v, want %+v", got, wantT)
	}
	wantT2 := TripLookup{ScheduleAvailable: true, Found: true, Trip: TripInfo{RouteID: "R", ServiceID: "WK"}}
	if got := store.LookupTrip(key, "T2"); got != wantT2 {
		t.Fatalf("empty block_id value = %+v, want %+v", got, wantT2)
	}
	wantMiss := TripLookup{ScheduleAvailable: true}
	if got := store.LookupTrip(key, "NOPE"); got != wantMiss {
		t.Fatalf("unknown trip = %+v, want %+v", got, wantMiss)
	}
	if got := NewScheduleStore().LookupTrip(key, "T"); got != (TripLookup{}) {
		t.Fatalf("missing schedule = %+v, want empty TripLookup", got)
	}

	// A trip's route, service and block don't go stale, so a failed refresh
	// leaves lookups answering from the retained snapshot while flagging Stale.
	store.MarkUnavailable(scheduleServer("A", "https://classify.zip"))
	wantStaleT := wantT
	wantStaleT.Stale = true
	if got := store.LookupTrip(key, "T"); got != wantStaleT {
		t.Fatalf("retained snapshot lookup = %+v, want %+v", got, wantStaleT)
	}
	wantStaleMiss := TripLookup{ScheduleAvailable: true, Stale: true}
	if got := store.LookupTrip(key, "NOPE"); got != wantStaleMiss {
		t.Fatalf("retained snapshot miss = %+v, want %+v", got, wantStaleMiss)
	}

	noColumn, noColumnKey := classifyStore(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))
	wantNoCol := TripLookup{ScheduleAvailable: true, Found: true, Trip: TripInfo{RouteID: "R", ServiceID: "WK"}}
	if got := noColumn.LookupTrip(noColumnKey, "T"); got != wantNoCol {
		t.Fatalf("missing block_id column = %+v, want %+v", got, wantNoCol)
	}
}

func TestCompileSchedulesSkipsTripIndexWhenDisabled(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	server := scheduleServer("A", "https://no-trip-index.zip")
	snapshots, err := compileSchedulesWithTripIndex(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}, false)
	if err != nil {
		t.Fatalf("compile without trip index: %v", err)
	}
	snapshot := snapshots[server.ServerKey()]
	if snapshot == nil || !snapshot.complete || snapshot.tripIndexRetained || len(snapshot.trips) != 0 {
		t.Fatalf("expected complete snapshot without retained trips, got %+v", snapshot)
	}

	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if available, active := store.Evaluate(server.ServerKey(), utc(2026, 9, 9, 8, 30)); !available || !active {
		t.Fatalf("Evaluate = available=%t active=%t, want true/true", available, active)
	}
	if got := store.LookupTrip(server.ServerKey(), "T"); got != (TripLookup{}) {
		t.Fatalf("LookupTrip without trip index = %+v, want empty TripLookup", got)
	}
	if got := store.ClassifyTrip(server.ServerKey(), TripDescriptor{TripID: "T"}, utc(2026, 9, 9, 8, 30)); got != (TripClassification{}) {
		t.Fatalf("ClassifyTrip without trip index = %+v, want empty TripClassification", got)
	}
}

// One upstream-merged feed can carry several agencies. Each agency's snapshot
// keeps only its own trips, and an agency-mode entry keeps only the configured
// agency's snapshot.
func TestCompileSchedulesSplitsTripsByAgencyInMergedFeed(t *testing.T) {
	files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
	files["agency.txt"] = "agency_id,agency_name,agency_url,agency_timezone\nA,Agency A,https://a.example,UTC\nB,Agency B,https://b.example,UTC\n"
	files["routes.txt"] = "route_id,agency_id,route_short_name,route_type\nRA,A,A,3\nRB,B,B,3\n"
	files["trips.txt"] = "route_id,service_id,trip_id\nRA,WK,TA\nRB,WK,TB\n"
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\nTA,08:00:00,08:00:00,S1,1\nTA,09:00:00,09:00:00,S2,2\nTB,08:00:00,08:00:00,S1,1\nTB,09:00:00,09:00:00,S2,2\n"
	server := scheduleServer("", "https://merged.zip")
	feed := downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}

	snapshots, err := compileSchedules(server, feed)
	if err != nil {
		t.Fatalf("server-mode compile: %v", err)
	}
	for agencyID, want := range map[string]string{"A": "TA", "B": "TB"} {
		snapshot := snapshots[models.ServerKey(server.ObaBaseURL, agencyID)]
		if snapshot == nil || len(snapshot.trips) != 1 {
			t.Fatalf("agency %s snapshot = %+v; want exactly one trip", agencyID, snapshot)
		}
		if _, ok := snapshot.trip(want); !ok {
			t.Fatalf("agency %s snapshot is missing its trip %q", agencyID, want)
		}
	}

	server.AgencyID = "A"
	snapshots, err = compileSchedules(server, feed)
	if err != nil {
		t.Fatalf("agency-mode compile: %v", err)
	}
	snapshotA := snapshots[models.ServerKey(server.ObaBaseURL, "A")]
	if len(snapshots) != 1 || snapshotA == nil || len(snapshotA.trips) != 1 || len(snapshotA.services) != 1 {
		t.Fatalf("agency-mode snapshots = %+v; want only agency A with 1 trip and 1 service", snapshots)
	}
}

// TestCalendarDatesOnlyRemovalDoesNotExtendCoverage covers #343: a removed
// date is not a service day, so it must not push a dates-only service's last
// service date forward, while an added date still does. ServiceDateRange
// keeps spanning every exception date.
func TestCalendarDatesOnlyRemovalDoesNotExtendCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dates       string
		wantExpired bool
	}{
		{"trailing removal", "service_id,date,exception_type\nWK,20260910,1\nWK,20261231,2\n", true},
		{"trailing addition", "service_id,date,exception_type\nWK,20260910,1\nWK,20261231,1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed := basicScheduleFiles("UTC", "12:00:00", "13:00:00")
			delete(feed, "calendar.txt")
			feed["calendar_dates.txt"] = tc.dates
			server := scheduleServer("A", "https://dates-only-removal.zip")
			snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			store := NewScheduleStore()
			store.Replace(server, snapshots)
			now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			if got := store.Expired(server.ServerKey(), now); got != tc.wantExpired {
				t.Fatalf("expired = %t, want %t", got, tc.wantExpired)
			}
			if got := store.CoversDate(server.ServerKey(), now); got == tc.wantExpired {
				t.Fatalf("covers date = %t, want %t", got, !tc.wantExpired)
			}
			_, latest, ok := store.ServiceDateRange(server.ServerKey())
			if want := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC); !ok || !latest.Equal(want) {
				t.Fatalf("service date range end = %s (ok=%t), want %s", latest, ok, want)
			}
		})
	}
}

// TestCalendarServiceKeepsEndDate checks that a calendar.txt service still
// covers dates up to its end_date.
func TestCalendarServiceKeepsEndDate(t *testing.T) {
	feed := basicScheduleFiles("UTC", "12:00:00", "13:00:00")
	feed["calendar.txt"] = "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,1,1,20260101,20261231\n"
	feed["calendar_dates.txt"] = "service_id,date,exception_type\nWK,20261231,2\n"
	server := scheduleServer("A", "https://calendar-end.zip")
	snapshots, err := compileSchedules(server, downloadedStaticFeed{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if store.Expired(server.ServerKey(), time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("a calendar.txt service expired before its end_date")
	}
}
