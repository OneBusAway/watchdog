package investigation

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
)

// invalidParams collects every problem with a request's parameters, so one 400
// response lists all of them instead of only the first.
type invalidParams []invalidParam

func (p *invalidParams) add(name, reason string) {
	*p = append(*p, invalidParam{Name: name, Reason: reason})
}

// writeInvalidParams writes the 400 response for p. Endpoints call it when p
// is not empty, before touching the database.
func writeInvalidParams(w http.ResponseWriter, p invalidParams) {
	writeProblem(w, http.StatusBadRequest, "Invalid query parameters",
		"One or more query parameters are invalid; see invalid_params.", p)
}

// parseQuery returns the request's query parameters. A name not in allowed, or
// a name given more than once, is added to bad: a typo must never return an
// unfiltered list. Problems are listed in name order, so the response is the
// same every time.
func parseQuery(r *http.Request, bad *invalidParams, allowed ...string) url.Values {
	values := r.URL.Query()
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		switch {
		case !slices.Contains(allowed, name):
			bad.add(name, "unknown parameter")
		case len(values[name]) > 1:
			bad.add(name, "must be given only once")
		}
	}
	return values
}

// required adds a problem for each of names the request left out.
func required(values url.Values, bad *invalidParams, names ...string) {
	for _, name := range names {
		if !values.Has(name) {
			bad.add(name, "is required")
		}
	}
}

// The param functions read one parameter. They return nil when it is absent,
// which is what sqlc's nullable query parameters expect, and add to bad when it
// is present but wrong.

// paramID reads a positive integer ID, written without a sign or leading
// zeros.
func paramID(values url.Values, name string, bad *invalidParams) *int64 {
	raw, ok := present(values, name, bad)
	if !ok {
		return nil
	}
	id, ok := parseID(raw)
	if !ok {
		bad.add(name, "must be a positive integer")
		return nil
	}
	return &id
}

// paramString reads a non-empty string.
func paramString(values url.Values, name string, bad *invalidParams) *string {
	raw, ok := present(values, name, bad)
	if !ok {
		return nil
	}
	return &raw
}

// paramBool reads true or false.
func paramBool(values url.Values, name string, bad *invalidParams) *bool {
	raw, ok := present(values, name, bad)
	if !ok {
		return nil
	}
	switch raw {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	}
	bad.add(name, "must be true or false")
	return nil
}

// paramEnum reads one of allowed.
func paramEnum(values url.Values, name string, allowed []string, bad *invalidParams) *string {
	raw, ok := present(values, name, bad)
	if !ok {
		return nil
	}
	if !slices.Contains(allowed, raw) {
		bad.add(name, "must be one of: "+strings.Join(allowed, ", "))
		return nil
	}
	return &raw
}

// paramTime reads an RFC 3339 time. It must carry an offset or Z: a time
// without one has no single meaning.
func paramTime(values url.Values, name string, bad *invalidParams) *time.Time {
	raw, ok := present(values, name, bad)
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		bad.add(name, "must be an RFC 3339 time with an offset or Z, for example 2026-10-24T14:05:30Z")
		return nil
	}
	return &t
}

// present returns the parameter's value when it was given. An empty value
// (?agency_id=) is reported as a problem rather than treated as absent.
func present(values url.Values, name string, bad *invalidParams) (string, bool) {
	if !values.Has(name) {
		return "", false
	}
	raw := values.Get(name)
	if raw == "" {
		bad.add(name, "must not be empty")
		return "", false
	}
	return raw, true
}

// notFuture adds a problem when t is after now. "What was open at tomorrow
// 9:00?" has no answer, and an empty list would read as "nothing was open".
// A nil t is not checked.
func notFuture(name string, t *time.Time, now time.Time, bad *invalidParams) {
	if t != nil && t.After(now) {
		bad.add(name, "must not be in the future")
	}
}

// pathID reads an ID from the path. ok is false when the segment is not a
// positive integer; the handler then returns 404, because the URI names no
// resource.
func pathID(ps httprouter.Params, name string) (id int64, ok bool) {
	return parseID(ps.ByName(name))
}

// parseID accepts only the canonical form of a positive integer: no sign, no
// leading zeros, so each resource has exactly one URI.
func parseID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}
