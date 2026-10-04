package gtfs

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// largeStopTimesFeed builds a schedule-valid feed whose stop_times.txt
// dominates its size, shaped like a production merged feed (long IDs and the
// optional columns real feeds carry). It returns the zip and the uncompressed
// size of stop_times.txt.
func largeStopTimesFeed(t *testing.T, trips, stopsPerTrip int) ([]byte, int) {
	t.Helper()
	var tripRows, stopRows, stopTimes strings.Builder
	tripRows.WriteString("route_id,service_id,trip_id\n")
	stopRows.WriteString("stop_id,stop_name,stop_lat,stop_lon\n")
	for s := 0; s < stopsPerTrip; s++ {
		fmt.Fprintf(&stopRows, "1_%08d,Stop %d,47.6,-122.3\n", s, s)
	}
	stopTimes.WriteString("trip_id,arrival_time,departure_time,stop_id,stop_sequence,stop_headsign,pickup_type,drop_off_type,shape_dist_traveled\n")
	for trip := 0; trip < trips; trip++ {
		fmt.Fprintf(&tripRows, "R,WK,1_%010d\n", trip)
		for s := 0; s < stopsPerTrip; s++ {
			clock := fmt.Sprintf("%02d:%02d:00", 6+s/60, s%60)
			fmt.Fprintf(&stopTimes, "1_%010d,%s,%s,1_%08d,%d,Downtown Seattle,0,0,%d.5\n", trip, clock, clock, s, s+1, s*100)
		}
	}
	files := basicScheduleFiles("America/Los_Angeles", "06:00:00", "07:00:00")
	files["trips.txt"] = tripRows.String()
	files["stops.txt"] = stopRows.String()
	files["stop_times.txt"] = stopTimes.String()
	return makeScheduleZip(t, files), stopTimes.Len()
}

// peakHeapGrowth runs fn while sampling the live heap and returns how far the
// heap rose above its starting point. A low GC percent keeps garbage from
// masquerading as live data, so the result tracks what fn holds onto.
func peakHeapGrowth(fn func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	base := stats.HeapAlloc

	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			var sample runtime.MemStats
			runtime.ReadMemStats(&sample)
			if sample.HeapAlloc > peak {
				peak = sample.HeapAlloc
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	fn()
	close(stop)
	<-done
	if peak < base {
		return 0
	}
	return peak - base
}

// Production merged feeds carry stop_times.txt files over 100 MB. Decoding the
// whole table into [][]string before walking it held several times the file
// size on the heap at once; with every static campaign parsing at startup that
// pushed Watchdog past its 2 GB memory limit. Validation needs only one row at
// a time, so the heap must not grow by more than the table's own size.
func TestParseRawScheduleFeedDoesNotMaterializeStopTimes(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large synthetic feed")
	}
	feed, stopTimesSize := largeStopTimesFeed(t, 4000, 100)
	server := scheduleServer("A", "https://large.zip")

	var raw rawScheduleFeed
	var parseErr error
	growth := peakHeapGrowth(func() {
		raw, parseErr = parseRawScheduleFeed(server.GtfsStaticFeeds[0], feed, server)
	})
	if parseErr != nil {
		t.Fatalf("parse: %v", parseErr)
	}
	if len(raw.trips) != 4000 || !raw.trips["1_0000000000"].hasStops {
		t.Fatalf("stop_times were not applied to trips: %d trips", len(raw.trips))
	}
	if growth > uint64(stopTimesSize) {
		t.Fatalf("parsing a %d MB stop_times.txt grew the heap by %d MB; want at most the table's own size",
			stopTimesSize>>20, growth>>20)
	}
}
