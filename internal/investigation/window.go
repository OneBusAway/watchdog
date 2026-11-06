package investigation

import (
	"net/url"
	"time"
)

const (
	// defaultWindow is the window a bounded list uses when none is given, so a
	// list with no window never scans 90 days of history.
	defaultWindow = 24 * time.Hour
	// maxWindow is the longest window one request may ask for.
	maxWindow = 31 * 24 * time.Hour
)

// window is a time range [From, To). Both are nil when the request has no
// window, and both are set otherwise.
type window struct {
	From, To *time.Time
}

// windowOptions say which pair of parameters a window comes from and which
// rules apply. Endpoints build them with the constructors below rather than
// by hand, so each kind of window follows the same rules everywhere.
type windowOptions struct {
	from, to string
	// required: the request must give the window (episode counts).
	required bool
	// bounded: at most maxWindow long.
	bounded bool
	// defaultLast24h: when absent, the window is the last 24 hours.
	defaultLast24h bool
}

// activeWindowOptions is the active_from/active_to window of the bounded
// episode lists. It defaults to the last 24 hours, except when status=open was
// requested (open episodes need no window) or when an opened window or open_at
// was given instead (they replace the default).
func activeWindowOptions(statusOpen, replaced bool) windowOptions {
	return windowOptions{
		from: "active_from", to: "active_to",
		bounded:        true,
		defaultLast24h: !statusOpen && !replaced,
	}
}

// openedWindowOptions is the opened_from/opened_to window of the episode
// lists: bounded, with no default.
func openedWindowOptions() windowOptions {
	return windowOptions{from: "opened_from", to: "opened_to", bounded: true}
}

// unboundedWindowOptions is an optional window with no default and no
// maximum, for lists that are small or scoped to one identity: the episodes of
// one stop, trip or vehicle (active_from/active_to), and the static GTFS
// version list (published_from/published_to).
func unboundedWindowOptions(from, to string) windowOptions {
	return windowOptions{from: from, to: to}
}

// countsWindowOptions is the required from/to window of episode counts.
func countsWindowOptions() windowOptions {
	return windowOptions{from: "from", to: "to", required: true, bounded: true}
}

// parseWindow reads the window opts describes, using the service clock.
func (s *InvestigationService) parseWindow(values url.Values, opts windowOptions, bad *invalidParams) window {
	return parseWindow(values, opts, s.now(), bad)
}

// parseWindow is InvestigationService.parseWindow with "now" explicit, so tests
// fix it.
//
// The two parameters come as a pair: one alone, or a start not before the
// end, is a problem. A bounded window may be at most 31 days long.
func parseWindow(values url.Values, opts windowOptions, now time.Time, bad *invalidParams) window {
	before := len(*bad)
	from := paramTime(values, opts.from, bad)
	to := paramTime(values, opts.to, bad)
	if len(*bad) > before {
		return window{}
	}

	switch {
	case from == nil && to == nil:
		if opts.required {
			bad.add(opts.from, "is required")
			bad.add(opts.to, "is required")
			return window{}
		}
		if opts.defaultLast24h {
			start := now.Add(-defaultWindow)
			return window{From: &start, To: &now}
		}
		return window{}
	case from == nil:
		bad.add(opts.from, "is required with "+opts.to)
		return window{}
	case to == nil:
		bad.add(opts.to, "is required with "+opts.from)
		return window{}
	case !from.Before(*to):
		bad.add(opts.to, "must be after "+opts.from)
		return window{}
	case opts.bounded && to.Sub(*from) > maxWindow:
		bad.add(opts.to, "must be within 31 days of "+opts.from)
		return window{}
	}
	return window{From: from, To: to}
}
