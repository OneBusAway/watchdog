package gtfs

import (
	"sort"
	"strings"
	"time"
)

// TripInfo is what the compiled schedule knows about one static trip.
// BlockID is empty when trips.txt has no block_id column or value.
type TripInfo struct {
	RouteID   string
	ServiceID string
	BlockID   string
}

// TripDescriptor carries the GTFS-Realtime trip fields that classification
// reads. StartDate (YYYYMMDD) and StartTime (HH:MM:SS, may exceed 24 hours)
// are optional and empty when the feed omits them.
type TripDescriptor struct {
	TripID    string
	StartDate string
	StartTime string
}

// ExpectedScheduleState and ExpectedScheduleReason values match the CHECK
// constraints on vehicle_position_evidence in the investigation schema.
type ExpectedScheduleState string

const (
	ExpectedScheduleActive   ExpectedScheduleState = "expected_active"
	ExpectedScheduleInactive ExpectedScheduleState = "expected_inactive"
	ExpectedScheduleUnknown  ExpectedScheduleState = "unknown"
)

type ExpectedScheduleReason string

const (
	ExpectedReasonNoTripDescriptor       ExpectedScheduleReason = "no_trip_descriptor"
	ExpectedReasonTripNotInStaticGTFS    ExpectedScheduleReason = "trip_not_in_static_gtfs"
	ExpectedReasonTripInstanceAmbiguous  ExpectedScheduleReason = "trip_instance_ambiguous"
	ExpectedReasonServiceNotActiveOnDate ExpectedScheduleReason = "service_not_active_on_date"
	ExpectedReasonBeforeScheduledWindow  ExpectedScheduleReason = "before_scheduled_window"
	ExpectedReasonAfterScheduledWindow   ExpectedScheduleReason = "after_scheduled_window"
	ExpectedReasonWithinScheduledWindow  ExpectedScheduleReason = "within_scheduled_window"
)

// TripClassification is the result of ClassifyTrip. When ScheduleAvailable is
// false no compiled schedule could be used and every other field is empty, so
// recording stores all four schedule columns as null. WindowStart and
// WindowEnd are in UTC and are zero when the result has no window.
type TripClassification struct {
	ScheduleAvailable bool
	State             ExpectedScheduleState
	Reason            ExpectedScheduleReason
	WindowStart       time.Time
	WindowEnd         time.Time
}

// LookupTrip returns the route, service and block of a static trip. It reads
// the retained complete snapshot even after a failed refresh has marked it
// unavailable: a trip's identity does not go stale the way "is it running
// now" does, and reporting a known trip as missing would be a false claim.
func (s *ScheduleStore) LookupTrip(serverKey, tripID string) (TripInfo, bool) {
	s.mu.RLock()
	state, ok := s.data[serverKey]
	s.mu.RUnlock()
	if !ok || state.snapshot == nil || !state.snapshot.complete {
		return TripInfo{}, false
	}
	trip, found := state.snapshot.trip(tripID)
	if !found {
		return TripInfo{}, false
	}
	return TripInfo{RouteID: trip.routeID, ServiceID: state.snapshot.services[trip.service].id, BlockID: trip.blockID}, true
}

// trip finds a trip by binary search. A sorted slice costs a fraction of a
// map's memory per trip, and a lookup is a handful of string comparisons.
func (s *ScheduleSnapshot) trip(id string) (scheduleTrip, bool) {
	i := sort.Search(len(s.trips), func(i int) bool { return s.trips[i].id >= id })
	if i < len(s.trips) && s.trips[i].id == id {
		return s.trips[i], true
	}
	return scheduleTrip{}, false
}

// ClassifyTrip reports whether the static schedule expected a trip to be
// running at the given time, following the "Expected schedule classification"
// algorithm in docs/historical-investigation-schema.md. Like Evaluate, it
// uses only a current snapshot: after a failed refresh the retained schedule
// may be outdated, so it reports that no schedule is available.
func (s *ScheduleStore) ClassifyTrip(serverKey string, descriptor TripDescriptor, at time.Time) TripClassification {
	s.mu.RLock()
	state, ok := s.data[serverKey]
	s.mu.RUnlock()
	if !ok || !state.available || state.snapshot == nil || !state.snapshot.complete {
		return TripClassification{}
	}
	return state.snapshot.classifyTrip(descriptor, at)
}

func (s *ScheduleSnapshot) classifyTrip(descriptor TripDescriptor, at time.Time) TripClassification {
	unknown := func(reason ExpectedScheduleReason) TripClassification {
		return TripClassification{ScheduleAvailable: true, State: ExpectedScheduleUnknown, Reason: reason}
	}
	if descriptor.TripID == "" {
		return unknown(ExpectedReasonNoTripDescriptor)
	}
	trip, found := s.trip(descriptor.TripID)
	if !found {
		return unknown(ExpectedReasonTripNotInStaticGTFS)
	}

	// A start date or time that is present but malformed cannot identify
	// which instance of the trip the vehicle is on.
	startDate, hasStartDate := 0, descriptor.StartDate != ""
	if hasStartDate {
		date, err := parseGTFSDate(descriptor.StartDate)
		if err != nil {
			return unknown(ExpectedReasonTripInstanceAmbiguous)
		}
		startDate = date
	}
	var startTime time.Duration
	hasStartTime := descriptor.StartTime != ""
	if hasStartTime {
		parsed, valid := parseGTFSClock(descriptor.StartTime)
		if !valid {
			return unknown(ExpectedReasonTripInstanceAmbiguous)
		}
		startTime = parsed
	}
	if trip.frequencyBased && !hasStartTime {
		return unknown(ExpectedReasonTripInstanceAmbiguous)
	}

	duration := time.Duration(trip.lastArrival-trip.firstDeparture) * time.Second
	if duration < 0 {
		// A one-stop trip whose arrival precedes its departure.
		duration = 0
	}
	offset := time.Duration(trip.firstDeparture) * time.Second
	if hasStartTime {
		offset = startTime
	}

	var days []time.Time
	if hasStartDate {
		days = []time.Time{dateFromNumber(startDate, s.timezone)}
	} else {
		local := at.In(s.timezone)
		back := int((offset + duration + 24*time.Hour - 1) / (24 * time.Hour))
		for i := 0; i <= back; i++ {
			days = append(days, time.Date(local.Year(), local.Month(), local.Day()-i, 0, 0, 0, 0, s.timezone))
		}
	}

	service := s.services[trip.service]
	var within, before, after *[2]time.Time
	for _, day := range days {
		if !service.activeOn(dateNumber(day), day.Weekday()) {
			continue
		}
		start := serviceTime(day, s.timezone, offset)
		window := [2]time.Time{start, start.Add(duration)}
		switch {
		case at.Before(window[0]):
			if before == nil || window[0].Before(before[0]) {
				before = &window
			}
		case at.After(window[1]):
			if after == nil || window[1].After(after[1]) {
				after = &window
			}
		default:
			if within == nil || window[0].After(within[0]) {
				within = &window
			}
		}
	}

	inactive := func(reason ExpectedScheduleReason, window *[2]time.Time) TripClassification {
		result := TripClassification{ScheduleAvailable: true, State: ExpectedScheduleInactive, Reason: reason}
		if window != nil {
			result.WindowStart, result.WindowEnd = window[0].UTC(), window[1].UTC()
		}
		return result
	}
	switch {
	case within != nil:
		return TripClassification{
			ScheduleAvailable: true, State: ExpectedScheduleActive, Reason: ExpectedReasonWithinScheduledWindow,
			WindowStart: within[0].UTC(), WindowEnd: within[1].UTC(),
		}
	case before != nil:
		return inactive(ExpectedReasonBeforeScheduledWindow, before)
	case after != nil:
		return inactive(ExpectedReasonAfterScheduledWindow, after)
	default:
		return inactive(ExpectedReasonServiceNotActiveOnDate, nil)
	}
}

// stringInterner shares one copy of each repeated ID across trip records and
// detaches it from the CSV line it was read from, which would otherwise stay
// on the heap for as long as the snapshot does.
type stringInterner map[string]string

func newStringInterner() stringInterner { return make(stringInterner) }

func (i stringInterner) intern(value string) string {
	if value == "" {
		return ""
	}
	if shared, ok := i[value]; ok {
		return shared
	}
	shared := strings.Clone(value)
	i[shared] = shared
	return shared
}
