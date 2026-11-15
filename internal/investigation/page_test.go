package investigation

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestLinkWithQuery(t *testing.T) {
	a := url.Values{}
	a.Set("opened_to", "2026-10-24T00:00:00Z")
	a.Set("agency_id", "3")
	a.Set("include", "opening_response,static_contexts")
	b := url.Values{}
	b.Set("include", "opening_response,static_contexts")
	b.Set("agency_id", "3")
	b.Set("opened_to", "2026-10-24T00:00:00Z")

	got := linkWithQuery("/v1/unmatched-stop-episodes", a)

	want := "/v1/unmatched-stop-episodes?agency_id=3&include=opening_response,static_contexts&opened_to=2026-10-24T00:00:00Z"
	if got != want {
		t.Errorf("link = %s\nwant   %s", got, want)
	}
	if other := linkWithQuery("/v1/unmatched-stop-episodes", b); other != got {
		t.Errorf("same parameters in another order gave %s", other)
	}
}

func TestLinkWithQuery_Escaping(t *testing.T) {
	params := url.Values{}
	params.Set("open_at", "2026-10-24T16:00:00+02:00")
	params.Set("route_id", "10 & 11")

	got := linkWithQuery("/v1/test", params)

	if want := "/v1/test?open_at=2026-10-24T16:00:00%2B02:00&route_id=10+%26+11"; got != want {
		t.Errorf("link = %s, want %s", got, want)
	}
	// The link must parse back to the same values.
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(u.Query(), params) {
		t.Errorf("round trip = %v, want %v", u.Query(), params)
	}
}

func TestLinkWithQuery_NoParams(t *testing.T) {
	if got := linkWithQuery("/v1/servers", url.Values{}); got != "/v1/servers" {
		t.Errorf("link = %s, want /v1/servers with no ?", got)
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	for _, id := range []int64{1, 4812, 9223372036854775807} {
		raw := encodeCursor(id)
		got, ok := decodeCursor(raw)
		if !ok || got != id {
			t.Errorf("decodeCursor(encodeCursor(%d)) = %d, %v", id, got, ok)
		}
	}
	if got := encodeCursor(4812); got != base64.RawURLEncoding.EncodeToString([]byte(`{"last_id":4812}`)) {
		t.Errorf("cursor = %s, want base64url of {\"last_id\":4812}", got)
	}
}

func TestCursor_Invalid(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		"not base64":    "%%%",
		"padded base64": base64.URLEncoding.EncodeToString([]byte(`{"last_id":1}`)),
		"not JSON":      enc("4812"),
		"wrong field":   enc(`{"id":4812}`),
		"extra field":   enc(`{"last_id":4812,"x":1}`),
		"zero":          enc(`{"last_id":0}`),
		"negative":      enc(`{"last_id":-5}`),
		"trailing data": enc(`{"last_id":1}{"last_id":2}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeCursor(raw); ok {
				t.Errorf("decodeCursor(%q) accepted", raw)
			}
		})
	}
}

func TestParsePage(t *testing.T) {
	cursor := encodeCursor(4812)
	cases := []struct {
		query   string
		size    int32
		afterID int64 // 0 means nil
		invalid []string
	}{
		{"", 50, 0, nil},
		{"page_size=1", 1, 0, nil},
		{"page_size=200", 200, 0, nil},
		{"page_size=2&cursor=" + cursor, 2, 4812, nil},
		{"page_size=0", 50, 0, []string{"page_size"}},
		{"page_size=201", 50, 0, []string{"page_size"}},
		{"page_size=ten", 50, 0, []string{"page_size"}},
		{"page_size=050", 50, 0, []string{"page_size"}},
		{"cursor=bogus", 50, 0, []string{"cursor"}},
		{"page_size=0&cursor=bogus", 50, 0, []string{"page_size", "cursor"}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			var bad invalidParams
			got := parsePage(values(t, tc.query), &bad)

			want := tc.invalid
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(names(bad), want) {
				t.Fatalf("invalid = %+v, want problems with %v", bad, want)
			}
			if got.Size != tc.size {
				t.Errorf("size = %d, want %d", got.Size, tc.size)
			}
			if (got.AfterID == nil) != (tc.afterID == 0) || (got.AfterID != nil && *got.AfterID != tc.afterID) {
				t.Errorf("after = %v, want %d", got.AfterID, tc.afterID)
			}
			if got.Limit() != got.Size+1 {
				t.Errorf("limit = %d, want size + 1", got.Limit())
			}
		})
	}
}

func TestTrimPage(t *testing.T) {
	p := page{Size: 2}

	rows, more := trimPage([]int{9, 8, 7}, p)
	if !reflect.DeepEqual(rows, []int{9, 8}) || !more {
		t.Errorf("3 rows for size 2 = %v, %v; want [9 8], true", rows, more)
	}
	rows, more = trimPage([]int{9, 8}, p)
	if !reflect.DeepEqual(rows, []int{9, 8}) || more {
		t.Errorf("2 rows for size 2 = %v, %v; want [9 8], false", rows, more)
	}
}

func TestPageLinks(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/v1/unmatched-stop-episodes?page_size=2&agency_id=3&cursor="+encodeCursor(4815), nil)

	last := pageLinks(req, 4812, false)
	if _, ok := last["next"]; ok {
		t.Errorf("last page has next: %v", last)
	}
	wantSelf := "/v1/unmatched-stop-episodes?agency_id=3&cursor=" + encodeCursor(4815) + "&page_size=2"
	if last["self"] != wantSelf {
		t.Errorf("self = %s, want %s", last["self"], wantSelf)
	}

	more := pageLinks(req, 4812, true)
	wantNext := "/v1/unmatched-stop-episodes?agency_id=3&cursor=" + encodeCursor(4812) + "&page_size=2"
	if more["next"] != wantNext {
		t.Errorf("next = %s, want %s", more["next"], wantNext)
	}
	if more["self"] != wantSelf {
		t.Errorf("self changed when next was added: %s", more["self"])
	}
}
