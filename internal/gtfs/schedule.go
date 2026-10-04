package gtfs

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

const maxScheduleOffset = 7 * 24 * time.Hour

type scheduleWindow struct {
	start      time.Duration
	end        time.Duration
	headway    time.Duration
	duration   time.Duration
	continuous bool
}

type scheduleService struct {
	weekdays uint8
	start    int
	end      int
	added    []int
	removed  []int
	windows  []scheduleWindow
}

// ScheduleSnapshot is the compact, immutable result of compiling the schedule
// tables of every configured static feed. It deliberately retains no trips,
// stops, or stop times.
type ScheduleSnapshot struct {
	timezone           *time.Location
	timezoneName       string
	services           []scheduleService
	feedURLs           []string
	maxOvernightOffset time.Duration
	complete           bool
}

type scheduleState struct {
	snapshot  *ScheduleSnapshot
	available bool
}

// ScheduleStore holds per-agency compiled schedules. A failed refresh leaves
// the last good immutable snapshot in place but marks it unavailable, ensuring
// stale or incomplete data cannot confidently classify an agency as outside
// service.
type ScheduleStore struct {
	mu   sync.RWMutex
	data map[string]scheduleState
	// preserve reports keys a server-scoped entry must not retire or mark
	// unavailable because a separately configured agency-scoped entry on the
	// same oba_base_url owns them. Nil preserves nothing.
	preserve func(key string) bool
}

func NewScheduleStore() *ScheduleStore {
	return &ScheduleStore{data: make(map[string]scheduleState)}
}

// Replace atomically publishes every schedule produced for one configured
// entry. Server mode also retires agencies that disappeared from the new,
// complete set; agency mode changes only its exact composite key.
func (s *ScheduleStore) Replace(server models.ObaServer, snapshots map[string]*ScheduleSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if server.IsServerScoped() {
		for key := range s.data {
			if server.OwnsServerKey(key) && !s.preserved(key) {
				delete(s.data, key)
			}
		}
	}
	for key, snapshot := range snapshots {
		s.data[key] = scheduleState{snapshot: snapshot, available: snapshot != nil && snapshot.complete}
	}
}

// MarkUnavailable preserves the last good snapshot while making its current
// validation/refresh status explicit. Missing state is naturally unavailable.
func (s *ScheduleStore) MarkUnavailable(server models.ObaServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.data {
		if (server.IsServerScoped() && server.OwnsServerKey(key) && !s.preserved(key)) || (!server.IsServerScoped() && key == server.ServerKey()) {
			state.available = false
			s.data[key] = state
		}
	}
}

func (s *ScheduleStore) preserved(key string) bool {
	return s.preserve != nil && s.preserve(key)
}

// Evaluate reports whether a complete current snapshot exists and whether at
// least one scheduled trip window contains now.
func (s *ScheduleStore) Evaluate(serverKey string, now time.Time) (available, active bool) {
	s.mu.RLock()
	state, ok := s.data[serverKey]
	s.mu.RUnlock()
	if !ok || !state.available || state.snapshot == nil || !state.snapshot.complete {
		return false, false
	}
	return true, state.snapshot.activeAt(now)
}

// CoversDate reports whether the retained complete per-agency snapshot has any
// service whose calendar or added exception reaches the agency-local date.
// It intentionally ignores state.available: a failed refresh can make the
// snapshot non-current while the retained complete bundle remains usable.
func (s *ScheduleStore) CoversDate(serverKey string, now time.Time) bool {
	s.mu.RLock()
	state, ok := s.data[serverKey]
	s.mu.RUnlock()
	if !ok || state.snapshot == nil || !state.snapshot.complete || state.snapshot.timezone == nil {
		return false
	}

	latest := 0
	for _, service := range state.snapshot.services {
		if service.end > latest {
			latest = service.end
		}
		if len(service.added) > 0 && service.added[len(service.added)-1] > latest {
			latest = service.added[len(service.added)-1]
		}
	}
	return latest > 0 && dateNumber(now.In(state.snapshot.timezone)) <= latest
}

// ServiceDateRange returns the earliest regular service end date and the
// latest date covered by either a regular service end or an added exception.
// The retained snapshot remains available for this purpose after a failed
// refresh even though its active-schedule state is marked unavailable.
func (s *ScheduleStore) ServiceDateRange(serverKey string) (earliest, latest time.Time, ok bool) {
	s.mu.RLock()
	state, exists := s.data[serverKey]
	s.mu.RUnlock()
	if !exists || state.snapshot == nil || !state.snapshot.complete || state.snapshot.timezone == nil {
		return time.Time{}, time.Time{}, false
	}

	earliestDate := 0
	latestDate := 0
	for _, service := range state.snapshot.services {
		if service.end > 0 && (earliestDate == 0 || service.end < earliestDate) {
			earliestDate = service.end
		}
		if service.end > latestDate {
			latestDate = service.end
		}
		if len(service.added) > 0 && service.added[len(service.added)-1] > latestDate {
			latestDate = service.added[len(service.added)-1]
		}
	}
	if earliestDate == 0 || latestDate == 0 {
		return time.Time{}, time.Time{}, false
	}
	return dateFromNumber(earliestDate, state.snapshot.timezone), dateFromNumber(latestDate, state.snapshot.timezone), true
}

func dateFromNumber(value int, location *time.Location) time.Time {
	year := value / 10000
	month := time.Month((value / 100) % 100)
	day := value % 100
	return time.Date(year, month, day, 0, 0, 0, 0, location)
}

func (s *ScheduleStore) Prune(keep func(string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for key := range s.data {
		if !keep(key) {
			removed = append(removed, key)
			delete(s.data, key)
		}
	}
	return removed
}

func (s *ScheduleSnapshot) activeAt(now time.Time) bool {
	localNow := now.In(s.timezone)
	daysBack := int(s.maxOvernightOffset/(24*time.Hour)) + 1
	for back := 0; back <= daysBack; back++ {
		day := localNow.AddDate(0, 0, -back)
		date := dateNumber(day)
		for _, service := range s.services {
			if !service.activeOn(date, day.Weekday()) {
				continue
			}
			for _, window := range service.windows {
				start := serviceTime(day, s.timezone, window.start)
				end := serviceTime(day, s.timezone, window.end)
				if now.Before(start) || now.After(end) {
					continue
				}
				if window.headway == 0 || window.continuous || frequencyActive(now, start, end, window.headway, window.duration) {
					return true
				}
			}
		}
	}
	return false
}

func frequencyActive(now, start, lastEnd time.Time, headway, duration time.Duration) bool {
	if duration >= headway {
		return true
	}
	lastDeparture := start.Add((now.Sub(start) / headway) * headway)
	if lastDeparture.After(lastEnd.Add(-duration)) {
		lastDeparture = lastEnd.Add(-duration)
	}
	return !now.After(lastDeparture.Add(duration))
}

func (s scheduleService) activeOn(date int, weekday time.Weekday) bool {
	if containsDate(s.removed, date) {
		return false
	}
	if containsDate(s.added, date) {
		return true
	}
	if date < s.start || date > s.end {
		return false
	}
	bit := uint8(1 << weekdayIndex(weekday))
	return s.weekdays&bit != 0
}

func containsDate(dates []int, date int) bool {
	i := sort.SearchInts(dates, date)
	return i < len(dates) && dates[i] == date
}

func weekdayIndex(day time.Weekday) int {
	if day == time.Sunday {
		return 6
	}
	return int(day) - 1
}

// GTFS defines a service day relative to noon-minus-12-hours, which avoids
// choosing an arbitrary side of a DST transition at local midnight.
func serviceTime(day time.Time, location *time.Location, offset time.Duration) time.Time {
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, location)
	return noon.Add(offset - 12*time.Hour)
}

func dateNumber(t time.Time) int {
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

type rawScheduleFeed struct {
	url      string
	agencies map[string]rawAgency
	routes   map[string]string
	stops    map[string]struct{}
	services map[string]*rawService
	trips    map[string]*rawTrip
}

type rawAgency struct {
	id       string
	location *time.Location
}

type rawService struct {
	weekdays uint8
	start    int
	end      int
	added    map[int]bool
	removed  map[int]bool
	// datesOnly marks a service defined solely by calendar_dates.txt. Its
	// start/end span every listed exception date (matching go-gtfs) so the
	// service date range reflects the last date, not the first row seen.
	datesOnly bool
}

type rawTrip struct {
	serviceID string
	agencyID  string
	first     rawEndpoint
	last      rawEndpoint
	hasStops  bool
	frequency []scheduleWindow
	sequences map[int]struct{}
}

type rawEndpoint struct {
	sequence  int
	arrival   time.Duration
	departure time.Duration
	valid     bool
}

func compileSchedules(server models.ObaServer, feeds []downloadedStaticFeed) (map[string]*ScheduleSnapshot, error) {
	if len(feeds) != len(server.GtfsStaticFeeds) {
		return nil, fmt.Errorf("only %d of %d configured static feeds succeeded", len(feeds), len(server.GtfsStaticFeeds))
	}
	compiled := make([]rawScheduleFeed, 0, len(feeds))
	for i, feed := range feeds {
		raw, err := parseRawScheduleFeed(feed.url, feed.data, server)
		if err != nil {
			return nil, fmt.Errorf("compile static feed %d (%s): %w", i, utils.SanitizeServerURL(feed.url), err)
		}
		compiled = append(compiled, raw)
	}
	return compileRawSchedules(server, compiled)
}

// compileRawSchedules compiles already-parsed raw feeds, in configured order,
// so a caller that validated each feed individually need not reparse them.
func compileRawSchedules(server models.ObaServer, compiled []rawScheduleFeed) (map[string]*ScheduleSnapshot, error) {
	if len(compiled) != len(server.GtfsStaticFeeds) {
		return nil, fmt.Errorf("only %d of %d configured static feeds succeeded", len(compiled), len(server.GtfsStaticFeeds))
	}

	type builder struct {
		timezone  *time.Location
		services  map[string]*scheduleService
		feeds     map[string]struct{}
		maxOffset time.Duration
	}
	builders := make(map[string]*builder)
	for feedIndex, feed := range compiled {
		for agencyID, agency := range feed.agencies {
			b := builders[agencyID]
			if b == nil {
				b = &builder{timezone: agency.location, services: make(map[string]*scheduleService), feeds: make(map[string]struct{})}
				builders[agencyID] = b
			} else if !sameTimezoneRules(b.timezone, agency.location) {
				return nil, fmt.Errorf("agency %q uses mixed timezones %q and %q", agencyID, b.timezone, agency.location)
			}
			b.feeds[utils.SanitizeServerURL(feed.url)] = struct{}{}
		}
		for tripID, trip := range feed.trips {
			service := feed.services[trip.serviceID]
			if service == nil {
				return nil, fmt.Errorf("trip %q references unknown service %q", tripID, trip.serviceID)
			}
			if !trip.hasStops || !trip.first.valid || !trip.last.valid {
				return nil, fmt.Errorf("trip %q has missing or invalid first/last stop times", tripID)
			}
			start := minDuration(trip.first.arrival, trip.first.departure)
			end := maxDuration(trip.last.arrival, trip.last.departure)
			if end < start || end > maxScheduleOffset {
				return nil, fmt.Errorf("trip %q has invalid endpoint range %s..%s", tripID, start, end)
			}
			windows := []scheduleWindow{{start: start, end: end}}
			if len(trip.frequency) > 0 {
				windows = windows[:0]
				duration := end - start
				for _, frequency := range trip.frequency {
					if frequency.continuous {
						frequency.end += duration
						if frequency.end > maxScheduleOffset {
							return nil, fmt.Errorf("trip %q frequency exceeds supported offset %s", tripID, maxScheduleOffset)
						}
						windows = append(windows, frequency)
						continue
					}
					span := frequency.end - frequency.start
					lastDeparture := frequency.start + ((span-time.Second)/frequency.headway)*frequency.headway
					frequency.duration = duration
					frequency.end = lastDeparture + duration
					if frequency.end > maxScheduleOffset {
						return nil, fmt.Errorf("trip %q frequency exceeds supported offset %s", tripID, maxScheduleOffset)
					}
					windows = append(windows, frequency)
				}
			}
			b := builders[trip.agencyID]
			if b == nil {
				return nil, fmt.Errorf("trip %q has unresolved agency %q", tripID, trip.agencyID)
			}
			serviceKey := fmt.Sprintf("%d|%s", feedIndex, trip.serviceID)
			compact := b.services[serviceKey]
			if compact == nil {
				added, removed := sortedExceptions(service)
				compact = &scheduleService{weekdays: service.weekdays, start: service.start, end: service.end, added: added, removed: removed}
				b.services[serviceKey] = compact
			}
			compact.windows = append(compact.windows, windows...)
			for _, window := range windows {
				if window.end > b.maxOffset {
					b.maxOffset = window.end
				}
			}
		}
	}

	if !server.IsServerScoped() {
		if builders[server.AgencyID] == nil {
			return nil, fmt.Errorf("configured agency %q is not attributable in the static feeds", server.AgencyID)
		}
		for agencyID := range builders {
			if agencyID != server.AgencyID {
				delete(builders, agencyID)
			}
		}
	}

	snapshots := make(map[string]*ScheduleSnapshot, len(builders))
	for agencyID, b := range builders {
		feeds := make([]string, 0, len(b.feeds))
		for feedURL := range b.feeds {
			feeds = append(feeds, feedURL)
		}
		sort.Strings(feeds)
		serviceKeys := make([]string, 0, len(b.services))
		for key := range b.services {
			serviceKeys = append(serviceKeys, key)
		}
		sort.Strings(serviceKeys)
		services := make([]scheduleService, 0, len(serviceKeys))
		for _, key := range serviceKeys {
			service := *b.services[key]
			service.windows = compactWindows(service.windows)
			services = append(services, service)
		}
		snapshots[models.ServerKey(server.ObaBaseURL, agencyID)] = &ScheduleSnapshot{
			timezone: b.timezone, timezoneName: b.timezone.String(), services: services,
			feedURLs: feeds, maxOvernightOffset: b.maxOffset, complete: true,
		}
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("no attributable agencies were compiled")
	}
	return snapshots, nil
}

func parseRawScheduleFeed(feedURL string, data []byte, server models.ObaServer) (rawScheduleFeed, error) {
	files, err := scheduleCSVFiles(data)
	if err != nil {
		return rawScheduleFeed{}, err
	}
	result := rawScheduleFeed{url: feedURL, agencies: make(map[string]rawAgency), routes: make(map[string]string), stops: make(map[string]struct{}), services: make(map[string]*rawService), trips: make(map[string]*rawTrip)}

	agencyRows, err := requiredTable(files, "agency.txt", "agency_timezone")
	if err != nil {
		return result, err
	}
	// Agencies in one feed must share a zone, but not necessarily its name:
	// San Diego's merged feed mixes America/Los_Angeles with its link
	// US/Pacific.
	var feedTimezone *time.Location
	for _, row := range agencyRows.rows {
		id := cell(row, agencyRows.index["agency_id"])
		zone := cell(row, agencyRows.index["agency_timezone"])
		if zone == "" {
			return result, fmt.Errorf("agency has no agency_timezone")
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			return result, fmt.Errorf("invalid agency_timezone %q: %w", zone, err)
		}
		if feedTimezone == nil {
			feedTimezone = location
		} else if !sameTimezoneRules(feedTimezone, location) {
			return result, fmt.Errorf("agency.txt contains mixed timezones %q and %q", feedTimezone, location)
		}
		effectiveID := id
		if effectiveID == "" && !server.IsServerScoped() && len(agencyRows.rows) == 1 {
			effectiveID = server.AgencyID
		}
		if effectiveID == "" {
			return result, fmt.Errorf("agency_id is required for server-mode or multi-agency feeds")
		}
		if _, duplicate := result.agencies[effectiveID]; duplicate {
			return result, fmt.Errorf("duplicate agency_id %q", effectiveID)
		}
		result.agencies[effectiveID] = rawAgency{id: effectiveID, location: location}
	}
	if len(result.agencies) == 0 {
		return result, fmt.Errorf("agency.txt has no rows")
	}

	routeRows, err := requiredTable(files, "routes.txt", "route_id")
	if err != nil {
		return result, err
	}
	for _, row := range routeRows.rows {
		routeID := cell(row, routeRows.index["route_id"])
		agencyID := cell(row, routeRows.index["agency_id"])
		if agencyID == "" && len(result.agencies) == 1 {
			for id := range result.agencies {
				agencyID = id
			}
		}
		if routeID == "" || result.agencies[agencyID].id == "" {
			return result, fmt.Errorf("route has missing or unknown agency attribution")
		}
		if _, duplicate := result.routes[routeID]; duplicate {
			return result, fmt.Errorf("duplicate route_id %q", routeID)
		}
		result.routes[routeID] = agencyID
	}
	stopRows, err := requiredTable(files, "stops.txt", "stop_id")
	if err != nil {
		return result, err
	}
	for _, row := range stopRows.rows {
		stopID := cell(row, stopRows.index["stop_id"])
		if stopID == "" {
			return result, fmt.Errorf("stop has no stop_id")
		}
		if _, duplicate := result.stops[stopID]; duplicate {
			return result, fmt.Errorf("duplicate stop_id %q", stopID)
		}
		result.stops[stopID] = struct{}{}
	}

	if err := parseRawServices(files, &result); err != nil {
		return result, err
	}
	tripRows, err := requiredTable(files, "trips.txt", "route_id", "service_id", "trip_id")
	if err != nil {
		return result, err
	}
	for _, row := range tripRows.rows {
		tripID := cell(row, tripRows.index["trip_id"])
		routeID := cell(row, tripRows.index["route_id"])
		serviceID := cell(row, tripRows.index["service_id"])
		agencyID := result.routes[routeID]
		if tripID == "" || agencyID == "" || result.services[serviceID] == nil {
			return result, fmt.Errorf("trip %q has invalid route or service reference", tripID)
		}
		if _, duplicate := result.trips[tripID]; duplicate {
			return result, fmt.Errorf("duplicate trip_id %q", tripID)
		}
		result.trips[tripID] = &rawTrip{serviceID: serviceID, agencyID: agencyID, sequences: make(map[int]struct{})}
	}
	if err := parseRawStopTimes(files, &result); err != nil {
		return result, err
	}
	if err := parseRawFrequencies(files, &result); err != nil {
		return result, err
	}
	return result, nil
}

type csvTable struct {
	index map[string]int
	rows  [][]string
}

// scheduleTables are the only GTFS files the schedule compiler reads. Large
// unrelated tables such as shapes.txt are never decoded into memory, and a
// malformed optional file cannot block static publication.
var scheduleTables = map[string]struct{}{
	"agency.txt": {}, "routes.txt": {}, "stops.txt": {}, "trips.txt": {},
	"stop_times.txt": {}, "calendar.txt": {}, "calendar_dates.txt": {}, "frequencies.txt": {},
}

func scheduleCSVFiles(data []byte) (map[string]csvTable, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := make(map[string]csvTable)
	for _, file := range zr.File {
		name := strings.TrimPrefix(file.Name, "./")
		if _, needed := scheduleTables[name]; !needed {
			continue
		}
		body, err := file.Open()
		if err != nil {
			return nil, err
		}
		reader := csv.NewReader(body)
		// Rows shorter than the header read as blank cells (see cell), rather
		// than rejecting the whole feed.
		reader.FieldsPerRecord = -1
		records, readErr := reader.ReadAll()
		closeErr := body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("%s is empty", name)
		}
		index := make(map[string]int, len(records[0]))
		for i, column := range records[0] {
			index[strings.TrimPrefix(strings.TrimSpace(column), "\ufeff")] = i
		}
		files[name] = csvTable{index: index, rows: records[1:]}
	}
	return files, nil
}

func requiredTable(files map[string]csvTable, name string, required ...string) (csvTable, error) {
	table, ok := files[name]
	if !ok {
		return csvTable{}, fmt.Errorf("missing %s", name)
	}
	for _, column := range required {
		if _, ok := table.index[column]; !ok {
			return csvTable{}, fmt.Errorf("%s is missing %s", name, column)
		}
	}
	for _, optional := range []string{"agency_id", "arrival_time", "departure_time"} {
		if _, ok := table.index[optional]; !ok {
			table.index[optional] = -1
		}
	}
	return table, nil
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func parseRawServices(files map[string]csvTable, result *rawScheduleFeed) error {
	calendar, hasCalendar := files["calendar.txt"]
	dates, hasDates := files["calendar_dates.txt"]
	if !hasCalendar && !hasDates {
		return fmt.Errorf("missing both calendar.txt and calendar_dates.txt")
	}
	if hasCalendar {
		required := []string{"service_id", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "start_date", "end_date"}
		for _, column := range required {
			if _, ok := calendar.index[column]; !ok {
				return fmt.Errorf("calendar.txt is missing %s", column)
			}
		}
		for _, row := range calendar.rows {
			id := cell(row, calendar.index["service_id"])
			if id == "" || result.services[id] != nil {
				return fmt.Errorf("missing or duplicate calendar service_id %q", id)
			}
			service := &rawService{added: make(map[int]bool), removed: make(map[int]bool)}
			for i, day := range required[1:8] {
				value := cell(row, calendar.index[day])
				if value != "0" && value != "1" {
					return fmt.Errorf("service %q has invalid %s value %q", id, day, value)
				}
				if value == "1" {
					service.weekdays |= 1 << i
				}
			}
			var err error
			service.start, err = parseGTFSDate(cell(row, calendar.index["start_date"]))
			if err != nil {
				return fmt.Errorf("service %q start_date: %w", id, err)
			}
			service.end, err = parseGTFSDate(cell(row, calendar.index["end_date"]))
			if err != nil || service.end < service.start {
				return fmt.Errorf("service %q has invalid end_date", id)
			}
			result.services[id] = service
		}
	}
	if hasDates {
		for _, column := range []string{"service_id", "date", "exception_type"} {
			if _, ok := dates.index[column]; !ok {
				return fmt.Errorf("calendar_dates.txt is missing %s", column)
			}
		}
		for _, row := range dates.rows {
			id := cell(row, dates.index["service_id"])
			if id == "" {
				return fmt.Errorf("calendar_dates row has no service_id")
			}
			date, err := parseGTFSDate(cell(row, dates.index["date"]))
			if err != nil {
				return fmt.Errorf("service %q exception date: %w", id, err)
			}
			service := result.services[id]
			if service == nil {
				service = &rawService{start: date, end: date, added: make(map[int]bool), removed: make(map[int]bool), datesOnly: true}
				result.services[id] = service
			} else if service.datesOnly {
				if date < service.start {
					service.start = date
				}
				if date > service.end {
					service.end = date
				}
			}
			if service.added[date] || service.removed[date] {
				return fmt.Errorf("service %q has duplicate exception date %d", id, date)
			}
			switch cell(row, dates.index["exception_type"]) {
			case "1":
				service.added[date] = true
			case "2":
				service.removed[date] = true
			default:
				return fmt.Errorf("service %q has invalid exception_type", id)
			}
		}
	}
	return nil
}

func parseRawStopTimes(files map[string]csvTable, result *rawScheduleFeed) error {
	table, err := requiredTable(files, "stop_times.txt", "trip_id", "stop_id", "stop_sequence")
	if err != nil {
		return err
	}
	if table.index["arrival_time"] < 0 || table.index["departure_time"] < 0 {
		return fmt.Errorf("stop_times.txt requires arrival_time and departure_time for endpoint validation")
	}
	for _, row := range table.rows {
		tripID := cell(row, table.index["trip_id"])
		trip := result.trips[tripID]
		if trip == nil {
			return fmt.Errorf("stop_times row references unknown trip %q", tripID)
		}
		stopID := cell(row, table.index["stop_id"])
		if _, ok := result.stops[stopID]; !ok {
			return fmt.Errorf("trip %q references unknown stop %q", tripID, stopID)
		}
		sequence, err := strconv.Atoi(cell(row, table.index["stop_sequence"]))
		if err != nil || sequence < 0 {
			return fmt.Errorf("trip %q has invalid stop_sequence", tripID)
		}
		if _, duplicate := trip.sequences[sequence]; duplicate {
			return fmt.Errorf("trip %q has duplicate stop_sequence %d", tripID, sequence)
		}
		trip.sequences[sequence] = struct{}{}
		arrivalText := cell(row, table.index["arrival_time"])
		departureText := cell(row, table.index["departure_time"])
		arrival, arrivalOK := parseGTFSClock(arrivalText)
		departure, departureOK := parseGTFSClock(departureText)
		if (arrivalText != "" && !arrivalOK) || (departureText != "" && !departureOK) {
			return fmt.Errorf("trip %q has invalid stop time", tripID)
		}
		endpoint := rawEndpoint{sequence: sequence, arrival: arrival, departure: departure, valid: arrivalOK && departureOK}
		if !trip.hasStops || sequence < trip.first.sequence {
			trip.first = endpoint
		}
		if !trip.hasStops || sequence > trip.last.sequence {
			trip.last = endpoint
		}
		trip.hasStops = true
	}
	return nil
}

func parseRawFrequencies(files map[string]csvTable, result *rawScheduleFeed) error {
	table, ok := files["frequencies.txt"]
	if !ok {
		return nil
	}
	for _, column := range []string{"trip_id", "start_time", "end_time", "headway_secs"} {
		if _, ok := table.index[column]; !ok {
			return fmt.Errorf("frequencies.txt is missing %s", column)
		}
	}
	for _, row := range table.rows {
		tripID := cell(row, table.index["trip_id"])
		trip := result.trips[tripID]
		if trip == nil {
			return fmt.Errorf("frequency references unknown trip %q", tripID)
		}
		start, startOK := parseGTFSClock(cell(row, table.index["start_time"]))
		end, endOK := parseGTFSClock(cell(row, table.index["end_time"]))
		headway, err := strconv.Atoi(cell(row, table.index["headway_secs"]))
		if !startOK || !endOK || end <= start || headway <= 0 || err != nil {
			return fmt.Errorf("trip %q has invalid frequency window", tripID)
		}
		exact := ""
		if index, ok := table.index["exact_times"]; ok {
			exact = cell(row, index)
			if exact != "" && exact != "0" && exact != "1" {
				return fmt.Errorf("trip %q has invalid exact_times", tripID)
			}
		}
		trip.frequency = append(trip.frequency, scheduleWindow{
			start: start, end: end, headway: time.Duration(headway) * time.Second,
			continuous: exact != "1",
		})
	}
	return nil
}

func parseGTFSClock(value string) (time.Duration, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || len(parts[0]) < 2 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, false
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	second, errS := strconv.Atoi(parts[2])
	if errH != nil || errM != nil || errS != nil || hour < 0 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return 0, false
	}
	offset := time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second
	return offset, offset <= maxScheduleOffset
}

func parseGTFSDate(value string) (int, error) {
	if len(value) != 8 {
		return 0, fmt.Errorf("invalid date %q", value)
	}
	parsed, err := time.Parse("20060102", value)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q", value)
	}
	return dateNumber(parsed), nil
}

func sortedExceptions(service *rawService) (added, removed []int) {
	for date := range service.added {
		added = append(added, date)
	}
	for date := range service.removed {
		removed = append(removed, date)
	}
	sort.Ints(added)
	sort.Ints(removed)
	return added, removed
}

func compactWindows(windows []scheduleWindow) []scheduleWindow {
	if len(windows) < 2 {
		return append([]scheduleWindow(nil), windows...)
	}
	var regular, frequency []scheduleWindow
	for _, window := range windows {
		if window.headway == 0 && !window.continuous {
			regular = append(regular, window)
		} else {
			frequency = append(frequency, window)
		}
	}
	sort.Slice(regular, func(i, j int) bool { return regular[i].start < regular[j].start })
	merged := make([]scheduleWindow, 0, len(windows))
	for _, window := range regular {
		if len(merged) == 0 {
			merged = append(merged, window)
			continue
		}
		last := &merged[len(merged)-1]
		if window.start <= last.end {
			if window.end > last.end {
				last.end = window.end
			}
			continue
		}
		merged = append(merged, window)
	}
	return append(merged, frequency...)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// timezoneRuleSpans are the instants over which sameTimezoneRules compares two
// zones: every day from 1970 through 2100, which includes years governed by
// each zone's final recurring rule, plus a sample of the far future.
var timezoneRuleSpans = [][2]time.Time{
	{time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC)},
	{time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2502, 1, 1, 0, 0, 0, 0, time.UTC)},
}

// sameTimezoneRules reports whether a and b are the same zone under different
// names, such as America/Los_Angeles and its backward-compatibility link
// US/Pacific. Go does not canonicalize links, so the zones are compared by
// their rules rather than their names: the UTC offset must agree once a day
// across timezoneRuleSpans, and wherever it changes, both zones must change at
// the same second. Zones that share an offset today but not their rules
// (America/Phoenix and America/Denver, or America/Los_Angeles and
// America/Tijuana) differ within that span. The two locations need not come
// from the same tz database: on a host without tzdata-legacy, a target such as
// America/Los_Angeles loads from the system while its link US/Pacific falls
// back to the tzdata embedded in the binary. If those releases disagree about
// the zone's rules (say, a DST change the host's newer tzdata already
// carries), a link and its target compare unequal and the feed is rejected;
// keeping the Go toolchain current keeps the embedded copy close to the host's.
// The daily sampling assumes no zone changes its offset twice within one day;
// no zone in the tz database does between 1970 and 2100.
//
// Time.ZoneBounds would avoid the sampling, but in years governed by a zone's
// final rule it reports stale period ends around the year boundary.
func sameTimezoneRules(a, b *time.Location) bool {
	if a.String() == b.String() {
		return true
	}
	for _, span := range timezoneRuleSpans {
		previous := span[0]
		previousOffset := utcOffset(previous, a)
		for t := previous; !t.After(span[1]); t = t.Add(24 * time.Hour) {
			offset := utcOffset(t, a)
			if offset != utcOffset(t, b) {
				return false
			}
			if offset != previousOffset && !offsetChange(previous, t, a).Equal(offsetChange(previous, t, b)) {
				return false
			}
			previous, previousOffset = t, offset
		}
	}
	return true
}

func utcOffset(t time.Time, location *time.Location) int {
	_, offset := t.In(location).Zone()
	return offset
}

// offsetChange returns the first second in (from, to] at which location's UTC
// offset differs from its offset at from. The caller guarantees it differs at
// to.
func offsetChange(from, to time.Time, location *time.Location) time.Time {
	before := utcOffset(from, location)
	for to.Sub(from) > time.Second {
		middle := from.Add((to.Sub(from) / 2).Truncate(time.Second))
		if utcOffset(middle, location) == before {
			from = middle
		} else {
			to = middle
		}
	}
	return to
}
