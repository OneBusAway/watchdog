package investigation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
)

// writeTestJSON runs writeJSON for a GET with the given If-None-Match, after
// setting the headers middleware.SecurityHeaders sets in production.
func writeTestJSON(t *testing.T, body any, cache cachePolicy, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	s, _ := newTestService(t)
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	rec.Header().Set("Pragma", "no-cache")
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	s.writeJSON(rec, req, body, cache)
	return rec
}

var etagPattern = regexp.MustCompile(`^"[0-9a-f]{32}"$`)

func TestWriteJSON_OK(t *testing.T) {
	rec := writeTestJSON(t, map[string]int{"id": 7}, cacheShort(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := h.Get("ETag"); !etagPattern.MatchString(got) {
		t.Errorf("ETag = %q, want 32 quoted hex characters", got)
	}
	if got := h.Get("Cache-Control"); got != "private, max-age=30" {
		t.Errorf("Cache-Control = %q, want it to replace no-store", got)
	}
	if got := h.Get("Pragma"); got != "" {
		t.Errorf("Pragma = %q, want it removed", got)
	}
	if got := h.Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none for cacheShort", got)
	}
	if got := rec.Body.String(); got != "{\"id\":7}\n" {
		t.Errorf("body = %q", got)
	}
	if got := h.Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length = %q, want %d", got, rec.Body.Len())
	}
}

func TestWriteJSON_ETag(t *testing.T) {
	body := map[string]string{"status": "open"}
	tag := writeTestJSON(t, body, cacheShort(), "").Header().Get("ETag")

	if again := writeTestJSON(t, body, cacheShort(), "").Header().Get("ETag"); again != tag {
		t.Errorf("same body gave ETags %s and %s", tag, again)
	}
	if other := writeTestJSON(t, map[string]string{"status": "closed"}, cacheShort(), "").Header().Get("ETag"); other == tag {
		t.Error("different bodies gave the same ETag")
	}

	cases := []struct {
		ifNoneMatch string
		want        int
	}{
		{tag, http.StatusNotModified},
		{"W/" + tag, http.StatusNotModified},
		{`"0000", ` + tag, http.StatusNotModified},
		{"*", http.StatusNotModified},
		{`"00000000000000000000000000000000"`, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.ifNoneMatch, func(t *testing.T) {
			rec := writeTestJSON(t, body, cacheShort(), tc.ifNoneMatch)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want != http.StatusNotModified {
				return
			}
			if rec.Body.Len() != 0 {
				t.Errorf("304 body = %q, want none", rec.Body.String())
			}
			if got := rec.Header().Get("ETag"); got != tag {
				t.Errorf("304 ETag = %q, want %q", got, tag)
			}
			if got := rec.Header().Get("Cache-Control"); got != "private, max-age=30" {
				t.Errorf("304 Cache-Control = %q", got)
			}
		})
	}
}

func TestWriteJSON_Immutable(t *testing.T) {
	closedAt := time.Date(2026, 10, 24, 16, 5, 30, 0, time.FixedZone("EET", 2*60*60))

	rec := writeTestJSON(t, map[string]int{"id": 1}, cacheImmutable(closedAt), "")

	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("Cache-Control = %q, want private, max-age=86400", got)
	}
	if got := rec.Header().Get("Last-Modified"); got != "Sat, 24 Oct 2026 14:05:30 GMT" {
		t.Errorf("Last-Modified = %q, want the time in GMT", got)
	}

	if got := writeTestJSON(t, map[string]int{"id": 1}, cacheImmutable(time.Time{}), "").Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none for the zero time", got)
	}
}

func TestWriteJSON_LinksNotEscaped(t *testing.T) {
	body := map[string]string{"next": "/v1/unmatched-stop-episodes?agency_id=3&cursor=abc"}

	rec := writeTestJSON(t, body, cacheShort(), "")

	want := "{\"next\":\"/v1/unmatched-stop-episodes?agency_id=3&cursor=abc\"}\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteJSON_EncodeError(t *testing.T) {
	rec := writeTestJSON(t, map[string]any{"bad": make(chan int)}, cacheShort(), "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	decodeProblem(t, rec)
}

// HEAD needs a real server: httptest.ResponseRecorder keeps the body, while
// net/http drops it.
func TestWriteJSON_Head(t *testing.T) {
	s, _ := newTestService(t)
	router := httprouter.New()
	handler := func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		s.writeJSON(w, r, map[string]string{"hello": "world"}, cacheShort())
	}
	router.GET("/v1/test", handler)
	router.HEAD("/v1/test", handler)
	server := httptest.NewServer(router)
	defer server.Close()

	get, err := http.Get(server.URL + "/v1/test")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	getBody, _ := io.ReadAll(get.Body)
	get.Body.Close()
	head, err := http.Head(server.URL + "/v1/test")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	headBody, _ := io.ReadAll(head.Body)
	head.Body.Close()

	if head.StatusCode != http.StatusOK || len(headBody) != 0 {
		t.Errorf("HEAD: status %d, body %q; want 200 and no body", head.StatusCode, headBody)
	}
	if head.Header.Get("ETag") != get.Header.Get("ETag") {
		t.Errorf("HEAD ETag %q differs from GET ETag %q", head.Header.Get("ETag"), get.Header.Get("ETag"))
	}
	if got := head.Header.Get("Content-Length"); got != strconv.Itoa(len(getBody)) {
		t.Errorf("HEAD Content-Length = %q, want %d", got, len(getBody))
	}
}

func TestWriteProblem_Shape(t *testing.T) {
	decode := func(invalid []invalidParam) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		writeProblem(rec, http.StatusBadRequest, "Invalid query parameters", "The time window may be at most 31 days.", invalid)
		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	body := decode([]invalidParam{{Name: "active_to", Reason: "must be within 31 days of active_from"}})
	for _, key := range []string{"type", "title", "status", "detail", "invalid_params"} {
		if _, ok := body[key]; !ok {
			t.Errorf("problem has no %q", key)
		}
	}
	if body["status"] != float64(http.StatusBadRequest) {
		t.Errorf("status = %v, want 400", body["status"])
	}

	if _, ok := decode(nil)["invalid_params"]; ok {
		t.Error("invalid_params present with no invalid parameters")
	}
}
