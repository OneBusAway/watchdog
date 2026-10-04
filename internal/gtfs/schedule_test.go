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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: feed}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: readFixture(t, "gtfs.zip")}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, feed)}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}})
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}})
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := basicScheduleFiles("UTC", "08:00:00", "09:00:00")
			tc.mutate(files)
			server := scheduleServer("A", "https://invalid.zip")
			if _, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}}); err == nil {
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}})
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

func TestCompileSchedulesAcrossFeedsComparesTimezoneRules(t *testing.T) {
	compile := func(first, second string) error {
		server := scheduleServer("A", "https://one.zip", "https://two.zip")
		_, err := compileSchedules(server, []downloadedStaticFeed{
			{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, basicScheduleFiles(first, "08:00:00", "09:00:00"))},
			{url: server.GtfsStaticFeeds[1], data: makeScheduleZip(t, basicScheduleFiles(second, "17:00:00", "18:00:00"))},
		})
		return err
	}
	if err := compile("America/Los_Angeles", "US/Pacific"); err != nil {
		t.Fatalf("aliased timezones across feeds must compile: %v", err)
	}
	if err := compile("America/Los_Angeles", "America/Tijuana"); err == nil {
		t.Fatal("different zones across feeds must not compile")
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

func TestCompileSchedulesMultipleFeedsAndAtomicAvailability(t *testing.T) {
	server := scheduleServer("A", "https://one.zip", "https://two.zip")
	feeds := []downloadedStaticFeed{
		{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, basicScheduleFiles("UTC", "08:00:00", "09:00:00"))},
		{url: server.GtfsStaticFeeds[1], data: makeScheduleZip(t, basicScheduleFiles("UTC", "17:00:00", "18:00:00"))},
	}
	snapshots, err := compileSchedules(server, feeds)
	if err != nil {
		t.Fatalf("compile both feeds: %v", err)
	}
	snapshot := snapshots[server.ServerKey()]
	if len(snapshot.feedURLs) != 2 || len(snapshot.services) != 2 {
		t.Fatalf("provenance/services not retained compactly: %+v", snapshot)
	}

	store := NewScheduleStore()
	store.Replace(server, snapshots)
	if available, _ := store.Evaluate(server.ServerKey(), time.Now()); !available {
		t.Fatal("complete replacement should be available")
	}
	if _, err := compileSchedules(server, feeds[:1]); err == nil {
		t.Fatal("partial configured-feed success must not compile")
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
	snapshots, err := compileSchedules(server, []downloadedStaticFeed{{url: server.GtfsStaticFeeds[0], data: makeScheduleZip(t, files)}})
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
