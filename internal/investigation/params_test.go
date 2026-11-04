package investigation

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
)

// values parses a raw query string such as "a=1&b=2".
func values(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return v
}

// names lists the parameter names in bad, in order.
func names(bad invalidParams) []string {
	out := []string{}
	for _, p := range bad {
		out = append(out, p.Name)
	}
	return out
}

func TestParseQuery_UnknownAndRepeated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/test?agency_id=3&agnecy_id=3&status=open&status=closed&zeta=1", nil)
	var bad invalidParams

	got := parseQuery(req, &bad, "agency_id", "status")

	want := invalidParams{
		{Name: "agnecy_id", Reason: "unknown parameter"},
		{Name: "status", Reason: "must be given only once"},
		{Name: "zeta", Reason: "unknown parameter"},
	}
	if !reflect.DeepEqual(bad, want) {
		t.Errorf("invalid = %+v, want %+v", bad, want)
	}
	if got.Get("agency_id") != "3" {
		t.Errorf("agency_id = %q, want 3", got.Get("agency_id"))
	}
}

func TestParseQuery_AllAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/test?agency_id=3", nil)
	var bad invalidParams

	parseQuery(req, &bad, "agency_id", "status")

	if len(bad) != 0 {
		t.Errorf("invalid = %+v, want none", bad)
	}
}

func TestParamID(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want int64 // 0 means nil
		bad  bool
	}{
		"absent":         {"", 0, false},
		"valid":          {"agency_id=42", 42, false},
		"empty":          {"agency_id=", 0, true},
		"zero":           {"agency_id=0", 0, true},
		"negative":       {"agency_id=-1", 0, true},
		"plus sign":      {"agency_id=+1", 0, true},
		"leading zero":   {"agency_id=042", 0, true},
		"not a number":   {"agency_id=abc", 0, true},
		"over int64 max": {"agency_id=9223372036854775808", 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var bad invalidParams
			got := paramID(values(t, tc.raw), "agency_id", &bad)

			if (len(bad) > 0) != tc.bad {
				t.Errorf("invalid = %+v, want problem: %v", bad, tc.bad)
			}
			switch {
			case tc.want == 0 && got != nil:
				t.Errorf("got %d, want nil", *got)
			case tc.want != 0 && (got == nil || *got != tc.want):
				t.Errorf("got %v, want %d", got, tc.want)
			}
		})
	}
}

func TestParamStringBoolEnum(t *testing.T) {
	var bad invalidParams
	v := values(t, "route_id=10&configured=true&status=open")

	if got := paramString(v, "route_id", &bad); got == nil || *got != "10" {
		t.Errorf("route_id = %v, want 10", got)
	}
	if got := paramBool(v, "configured", &bad); got == nil || !*got {
		t.Errorf("configured = %v, want true", got)
	}
	if got := paramEnum(v, "status", []string{"open", "closed"}, &bad); got == nil || *got != "open" {
		t.Errorf("status = %v, want open", got)
	}
	if paramString(v, "missing", &bad) != nil || paramBool(v, "missing", &bad) != nil {
		t.Error("an absent parameter must give nil")
	}
	if len(bad) != 0 {
		t.Fatalf("invalid = %+v, want none", bad)
	}

	v = values(t, "route_id=&configured=yes&status=resolved")
	paramString(v, "route_id", &bad)
	paramBool(v, "configured", &bad)
	paramEnum(v, "status", []string{"open", "closed"}, &bad)
	want := invalidParams{
		{Name: "route_id", Reason: "must not be empty"},
		{Name: "configured", Reason: "must be true or false"},
		{Name: "status", Reason: "must be one of: open, closed"},
	}
	if !reflect.DeepEqual(bad, want) {
		t.Errorf("invalid = %+v, want %+v", bad, want)
	}
}

func TestParamTime_RequiresOffset(t *testing.T) {
	cases := map[string]bool{ // value → valid
		"2026-10-24T14:05:30Z":           true,
		"2026-10-24T16:05:30%2B02:00":    true, // %2B is +
		"2026-10-24T14:05:30.123456789Z": true,
		"2026-10-24T14:05:30":            false, // no offset
		"2026-10-24":                     false,
		"yesterday":                      false,
	}
	for raw, valid := range cases {
		t.Run(raw, func(t *testing.T) {
			var bad invalidParams
			got := paramTime(values(t, "at="+raw), "at", &bad)

			if valid != (got != nil) || valid != (len(bad) == 0) {
				t.Errorf("got %v, invalid %+v; want valid: %v", got, bad, valid)
			}
		})
	}

	var bad invalidParams
	got := paramTime(values(t, "at=2026-10-24T16:05:30%2B02:00"), "at", &bad)
	if want := time.Date(2026, 10, 24, 14, 5, 30, 0, time.UTC); !got.Equal(want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
}

func TestNotFuture(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	var bad invalidParams

	notFuture("at", &past, now, &bad)
	notFuture("at", &now, now, &bad)
	notFuture("at", nil, now, &bad)
	if len(bad) != 0 {
		t.Fatalf("invalid = %+v; now, earlier and absent must pass", bad)
	}

	notFuture("open_at", &future, now, &bad)
	if want := (invalidParams{{Name: "open_at", Reason: "must not be in the future"}}); !reflect.DeepEqual(bad, want) {
		t.Errorf("invalid = %+v, want %+v", bad, want)
	}
}

func TestPathID_Malformed(t *testing.T) {
	cases := map[string]bool{ // segment → valid
		"4812":                true,
		"1":                   true,
		"0":                   false,
		"-4":                  false,
		"+4":                  false,
		"04812":               false,
		"abc":                 false,
		"":                    false,
		"9223372036854775808": false,
		"9223372036854775807": true,
	}
	for raw, valid := range cases {
		t.Run(raw, func(t *testing.T) {
			_, ok := pathID(httprouter.Params{{Key: "episode_id", Value: raw}}, "episode_id")

			if ok != valid {
				t.Errorf("ok = %v, want %v", ok, valid)
			}
		})
	}
}

func TestWriteInvalidParams(t *testing.T) {
	rec := httptest.NewRecorder()

	writeInvalidParams(rec, invalidParams{{Name: "agency_id", Reason: "must be a positive integer"}})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := decodeProblem(t, rec)
	if body.Title != "Invalid query parameters" || len(body.InvalidParams) != 1 || body.InvalidParams[0].Name != "agency_id" {
		t.Errorf("body = %+v", body)
	}
}
