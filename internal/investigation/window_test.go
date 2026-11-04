package investigation

import (
	"reflect"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func TestParseWindow(t *testing.T) {
	day := func(d int) time.Time { return testNow.AddDate(0, 0, d) }
	at := func(t time.Time) string { return t.Format(time.RFC3339) }
	bounded := activeWindowOptions(false, false)

	cases := []struct {
		name     string
		opts     windowOptions
		query    string
		from, to *time.Time // both nil means no window
		invalid  []string   // parameter names with problems
	}{
		{
			name: "default last 24 hours",
			opts: bounded,
			from: ptr(testNow.Add(-24 * time.Hour)), to: ptr(testNow),
		},
		{
			name:  "explicit window",
			opts:  bounded,
			query: "active_from=" + at(day(-3)) + "&active_to=" + at(day(-1)),
			from:  ptr(day(-3)), to: ptr(day(-1)),
		},
		{
			name:  "exactly 31 days",
			opts:  bounded,
			query: "active_from=" + at(day(-31)) + "&active_to=" + at(testNow),
			from:  ptr(day(-31)), to: ptr(testNow),
		},
		{
			name:    "over 31 days",
			opts:    bounded,
			query:   "active_from=" + at(day(-32)) + "&active_to=" + at(testNow),
			invalid: []string{"active_to"},
		},
		{
			name:    "from alone",
			opts:    bounded,
			query:   "active_from=" + at(day(-1)),
			invalid: []string{"active_to"},
		},
		{
			name:    "to alone",
			opts:    bounded,
			query:   "active_to=" + at(day(-1)),
			invalid: []string{"active_from"},
		},
		{
			name:    "start equals end",
			opts:    bounded,
			query:   "active_from=" + at(day(-1)) + "&active_to=" + at(day(-1)),
			invalid: []string{"active_to"},
		},
		{
			name:    "start after end",
			opts:    bounded,
			query:   "active_from=" + at(day(-1)) + "&active_to=" + at(day(-2)),
			invalid: []string{"active_to"},
		},
		{
			name:    "malformed time",
			opts:    bounded,
			query:   "active_from=yesterday&active_to=" + at(day(-1)),
			invalid: []string{"active_from"},
		},
		{
			name: "status=open has no default",
			opts: activeWindowOptions(true, false),
		},
		{
			name:    "status=open keeps the 31-day maximum",
			opts:    activeWindowOptions(true, false),
			query:   "active_from=" + at(day(-40)) + "&active_to=" + at(testNow),
			invalid: []string{"active_to"},
		},
		{
			name: "another window replaces the default",
			opts: activeWindowOptions(false, true),
		},
		{
			name:    "a replaced default keeps the pair rule",
			opts:    activeWindowOptions(false, true),
			query:   "active_from=" + at(day(-1)),
			invalid: []string{"active_to"},
		},
		{
			name: "opened window has no default",
			opts: openedWindowOptions(),
		},
		{
			name:    "opened window is bounded",
			opts:    openedWindowOptions(),
			query:   "opened_from=" + at(day(-32)) + "&opened_to=" + at(testNow),
			invalid: []string{"opened_to"},
		},
		{
			name:  "unbounded has no maximum",
			opts:  unboundedWindowOptions("published_from", "published_to"),
			query: "published_from=" + at(day(-365)) + "&published_to=" + at(testNow),
			from:  ptr(day(-365)), to: ptr(testNow),
		},
		{
			name: "unbounded has no default",
			opts: unboundedWindowOptions("active_from", "active_to"),
		},
		{
			name:    "counts window is required",
			opts:    countsWindowOptions(),
			invalid: []string{"from", "to"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bad invalidParams
			got := parseWindow(values(t, tc.query), tc.opts, testNow, &bad)

			want := tc.invalid
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(names(bad), want) {
				t.Fatalf("invalid = %+v, want problems with %v", bad, want)
			}
			if !sameTime(got.From, tc.from) || !sameTime(got.To, tc.to) {
				t.Errorf("window = %v–%v, want %v–%v", got.From, got.To, tc.from, tc.to)
			}
		})
	}
}

// The method uses the service clock, which tests can replace.
func TestParseWindow_ServiceClock(t *testing.T) {
	s, _ := newTestService(t)
	s.now = func() time.Time { return testNow }
	var bad invalidParams

	got := s.parseWindow(values(t, ""), activeWindowOptions(false, false), &bad)

	if got.To == nil || !got.To.Equal(testNow) {
		t.Errorf("default window ends at %v, want %v", got.To, testNow)
	}
}

func ptr(t time.Time) *time.Time { return &t }

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
