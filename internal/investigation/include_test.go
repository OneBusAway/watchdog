package investigation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"watchdog.onebusaway.org/internal/db/dbgen"
)

const (
	kindResponse       resourceKind = "test_response"
	kindStaticContexts resourceKind = "test_static_contexts"
	kindNoLoader       resourceKind = "test_no_loader"
)

// An episode-like endpoint: two links to responses, one to-many link, and one
// link whose kind has no loader yet.
var testIncludable = map[string]resourceKind{
	"opening_response": kindResponse,
	"closing_response": kindResponse,
	"static_contexts":  kindStaticContexts,
	"rule_version":     kindNoLoader,
}

// fakeLoader records every call and returns {"id": N} for each ID.
type fakeLoader struct {
	calls [][]int64
}

func (f *fakeLoader) load(_ context.Context, _ *dbgen.Queries, ids []int64) (map[int64]any, error) {
	f.calls = append(f.calls, append([]int64(nil), ids...))
	out := map[int64]any{}
	for _, id := range ids {
		out[id] = map[string]int64{"id": id}
	}
	return out, nil
}

func newIncludeService(t *testing.T) (*InvestigationService, *fakeLoader, *fakeLoader) {
	t.Helper()
	s, _ := newTestService(t)
	responses, contexts := &fakeLoader{}, &fakeLoader{}
	s.registerLoader(kindResponse, responses.load)
	s.registerLoader(kindStaticContexts, contexts.load)
	return s, responses, contexts
}

func TestParseInclude(t *testing.T) {
	s, _, _ := newIncludeService(t)
	cases := []struct {
		query   string
		names   []string
		reasons []string
	}{
		{"", nil, nil},
		{"include=opening_response", []string{"opening_response"}, nil},
		{"include=static_contexts,closing_response", []string{"closing_response", "static_contexts"}, nil},
		{"include=", nil, []string{"must not be empty"}},
		{"include=opening_response,,static_contexts", []string{"opening_response", "static_contexts"}, []string{"must not contain an empty name"}},
		{"include=opening_response,", []string{"opening_response"}, []string{"must not contain an empty name"}},
		{"include=opening_response,opening_response", []string{"opening_response"}, []string{"lists opening_response more than once"}},
		{"include=agency", []string{}, []string{"cannot include agency; can include: closing_response, opening_response, static_contexts"}},
		{"include=self", []string{}, []string{"cannot include self; can include: closing_response, opening_response, static_contexts"}},
		{"include=rule_version", []string{}, []string{"cannot include rule_version; can include: closing_response, opening_response, static_contexts"}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			var bad invalidParams
			got := s.parseInclude(values(t, tc.query), testIncludable, &bad)

			var reasons []string
			for _, p := range bad {
				if p.Name != "include" {
					t.Errorf("problem named %q, want include", p.Name)
				}
				reasons = append(reasons, p.Reason)
			}
			if !reflect.DeepEqual(reasons, tc.reasons) {
				t.Errorf("reasons = %q, want %q", reasons, tc.reasons)
			}
			if !reflect.DeepEqual(got.names, tc.names) {
				t.Errorf("names = %q, want %q", got.names, tc.names)
			}
			for _, name := range tc.names {
				if !got.has(name) {
					t.Errorf("has(%q) = false", name)
				}
			}
		})
	}
}

func TestParseInclude_NothingIncludable(t *testing.T) {
	s, _ := newTestService(t) // no loaders registered
	var bad invalidParams

	s.parseInclude(values(t, "include=opening_response"), testIncludable, &bad)

	if len(bad) != 1 || !strings.HasSuffix(bad[0].Reason, "nothing can be included here") {
		t.Errorf("invalid = %+v, want one problem saying nothing can be included", bad)
	}
}

// Three episodes from one poll: two share their opening response, one is still
// open (no closing_response), and each has its own static contexts.
func testItems() []itemLinks {
	response := func(id int64, url string) related { return related{Kind: kindResponse, ID: id, URL: url} }
	contexts := func(id int64, url string) related { return related{Kind: kindStaticContexts, ID: id, URL: url} }
	return []itemLinks{
		{
			"self":             {URL: "/v1/unmatched-stop-episodes/3"},
			"opening_response": response(100, "/v1/oba-metrics-responses/100"),
			"closing_response": response(105, "/v1/oba-metrics-responses/105"),
			"static_contexts":  contexts(3, "/v1/unmatched-stop-episodes/3/static-contexts"),
		},
		{
			"self":             {URL: "/v1/unmatched-stop-episodes/2"},
			"opening_response": response(100, "/v1/oba-metrics-responses/100"),
			"static_contexts":  contexts(2, "/v1/unmatched-stop-episodes/2/static-contexts"),
		},
		{
			"self":             {URL: "/v1/unmatched-stop-episodes/1"},
			"opening_response": response(90, "/v1/oba-metrics-responses/90"),
			"closing_response": response(100, "/v1/oba-metrics-responses/100"),
			"static_contexts":  contexts(1, "/v1/unmatched-stop-episodes/1/static-contexts"),
		},
	}
}

func TestLoadIncluded(t *testing.T) {
	s, responses, contexts := newIncludeService(t)
	var bad invalidParams
	inc := s.parseInclude(values(t, "include=opening_response,closing_response"), testIncludable, &bad)

	got, err := s.loadIncluded(context.Background(), nil, testItems(), inc)

	if err != nil {
		t.Fatalf("loadIncluded: %v", err)
	}
	// Opening and closing responses are one kind: one call, distinct IDs.
	if want := [][]int64{{90, 100, 105}}; !reflect.DeepEqual(responses.calls, want) {
		t.Errorf("response loader calls = %v, want %v", responses.calls, want)
	}
	if len(contexts.calls) != 0 {
		t.Errorf("static contexts loaded without being requested: %v", contexts.calls)
	}
	// Response 100 is linked three times and appears once.
	want := included{
		"/v1/oba-metrics-responses/90":  map[string]int64{"id": 90},
		"/v1/oba-metrics-responses/100": map[string]int64{"id": 100},
		"/v1/oba-metrics-responses/105": map[string]int64{"id": 105},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("included = %v, want %v", got, want)
	}
}

func TestLoadIncluded_ToManyLink(t *testing.T) {
	s, responses, contexts := newIncludeService(t)
	var bad invalidParams
	inc := s.parseInclude(values(t, "include=static_contexts,opening_response"), testIncludable, &bad)

	got, err := s.loadIncluded(context.Background(), nil, testItems(), inc)

	if err != nil {
		t.Fatalf("loadIncluded: %v", err)
	}
	if want := [][]int64{{1, 2, 3}}; !reflect.DeepEqual(contexts.calls, want) {
		t.Errorf("static contexts loader calls = %v, want %v", contexts.calls, want)
	}
	if len(responses.calls) != 1 {
		t.Errorf("response loader calls = %d, want 1", len(responses.calls))
	}
	if len(got) != 5 { // 3 static-context collections + responses 90 and 100
		t.Errorf("included has %d entries, want 5: %v", len(got), got)
	}
}

func TestLoadIncluded_AbsentLinks(t *testing.T) {
	s, responses, _ := newIncludeService(t)
	var bad invalidParams
	inc := s.parseInclude(values(t, "include=closing_response"), testIncludable, &bad)
	openOnly := []itemLinks{testItems()[1]} // still open: no closing_response

	got, err := s.loadIncluded(context.Background(), nil, openOnly, inc)

	if err != nil {
		t.Fatalf("loadIncluded: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("included = %v, want an empty, non-nil object", got)
	}
	if len(responses.calls) != 0 {
		t.Errorf("loader called with nothing to load: %v", responses.calls)
	}
}

// The included key appears exactly when the request had include.
func TestIncluded_KeyOnlyWhenRequested(t *testing.T) {
	s, _, _ := newIncludeService(t)
	type body struct {
		Items    []int    `json:"items"`
		Included included `json:"included,omitzero"`
	}

	none, err := s.loadIncluded(context.Background(), nil, testItems(), include{})
	if err != nil {
		t.Fatalf("loadIncluded: %v", err)
	}
	if got := mustJSON(t, body{Items: []int{}, Included: none}); got != `{"items":[]}` {
		t.Errorf("without include: %s, want no included key", got)
	}

	var bad invalidParams
	inc := s.parseInclude(values(t, "include=closing_response"), testIncludable, &bad)
	empty, err := s.loadIncluded(context.Background(), nil, []itemLinks{testItems()[1]}, inc)
	if err != nil {
		t.Fatalf("loadIncluded: %v", err)
	}
	if got := mustJSON(t, body{Items: []int{}, Included: empty}); got != `{"items":[],"included":{}}` {
		t.Errorf("with include and nothing found: %s, want an empty included object", got)
	}
}

func TestLoadIncluded_LoaderErrors(t *testing.T) {
	s, _ := newTestService(t)
	boom := errors.New("boom")
	s.registerLoader(kindResponse, func(context.Context, *dbgen.Queries, []int64) (map[int64]any, error) {
		return nil, boom
	})
	s.registerLoader(kindStaticContexts, func(context.Context, *dbgen.Queries, []int64) (map[int64]any, error) {
		return map[int64]any{}, nil // returns nothing for the IDs asked
	})
	var bad invalidParams

	_, err := s.loadIncluded(context.Background(), nil, testItems(), s.parseInclude(values(t, "include=opening_response"), testIncludable, &bad))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the loader's error", err)
	}
	_, err = s.loadIncluded(context.Background(), nil, testItems(), s.parseInclude(values(t, "include=static_contexts"), testIncludable, &bad))
	if err == nil || !strings.Contains(err.Error(), "returned nothing") {
		t.Errorf("err = %v, want an error for a missing representation", err)
	}
}

func TestRegisterLoader_Twice(t *testing.T) {
	s, _ := newTestService(t)
	load := (&fakeLoader{}).load
	s.registerLoader(kindResponse, load)

	defer func() {
		if recover() == nil {
			t.Error("registering a kind twice did not panic")
		}
	}()
	s.registerLoader(kindResponse, load)
}

func TestItemLinks_Links(t *testing.T) {
	got := testItems()[1].links()

	want := links{
		"self":             "/v1/unmatched-stop-episodes/2",
		"opening_response": "/v1/oba-metrics-responses/100",
		"static_contexts":  "/v1/unmatched-stop-episodes/2/static-contexts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v, want %v", got, want)
	}
}
