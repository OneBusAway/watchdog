package investigation

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"watchdog.onebusaway.org/internal/db/dbgen"
)

// Including related resources (?include=) works in three parts:
//
//  1. Each target endpoint registers one loader for its resource kind, in
//     registerRoutes next to its s.get line.
//  2. Each endpoint that links to other resources declares which link names
//     it can carry and the kind each points to (an includable map), and passes
//     it to parseInclude.
//  3. After building its items, the endpoint calls loadIncluded with every
//     item's links. It makes one loader call per kind, however many items and
//     link names point to that kind.
//
// So once a kind has a loader, every endpoint that links to it can include it
// by adding one entry to its includable map.

// resourceKind names one kind of resource that can be included, such as an
// OBA metrics response or an episode's static contexts.
type resourceKind string

// loader returns the representation of each resource of one kind, by ID, in a
// single query (WHERE id = ANY($1)). Each value must be exactly what a GET on
// that resource's URL returns, without its own included object, so a client
// sees the same thing whether it follows the link or reads included.
//
// For a to-many link such as static_contexts or evidence, the ID is the
// parent episode's and the value is the whole collection representation.
//
// A loader must return every ID it is given: a link exists only when its
// target does.
type loader func(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error)

// registerLoader adds the loader for kind. It is called while routes are
// registered, before any request is served, and the registry is only read
// afterwards. Registering a kind twice is a programming error and panics.
func (s *InvestigationService) registerLoader(kind resourceKind, load loader) {
	if _, ok := s.loaders[kind]; ok {
		panic(fmt.Sprintf("investigation: loader for %q registered twice", kind))
	}
	s.loaders[kind] = load
}

// related is one link of an item: the target's kind, its ID and its URL.
type related struct {
	Kind resourceKind
	ID   int64
	URL  string
}

// itemLinks is everything one item links to, by link name. An endpoint builds
// it once per item and uses it twice: links() for the item's links object, and
// as input to loadIncluded. A link whose target does not exist is not added.
type itemLinks map[string]related

// links is the item's links object: link name to URL.
func (l itemLinks) links() links {
	out := make(links, len(l))
	for name, r := range l {
		out[name] = r.URL
	}
	return out
}

// include is the set of link names a request asked to include.
type include struct {
	names []string // sorted
}

// has reports whether name was requested.
func (inc include) has(name string) bool {
	_, found := slices.BinarySearch(inc.names, name)
	return found
}

// requested reports whether the request had an include parameter at all.
func (inc include) requested() bool {
	return inc.names != nil
}

// parseInclude reads the include parameter: comma-separated link names. A name
// is accepted only when it is in includable and its kind has a registered
// loader. Unknown names, repeats and empty names are added to bad.
func (s *InvestigationService) parseInclude(values url.Values, includable map[string]resourceKind, bad *invalidParams) include {
	raw, ok := present(values, "include", bad)
	if !ok {
		return include{}
	}
	names := []string{}
	for _, name := range strings.Split(raw, ",") {
		switch {
		case name == "":
			bad.add("include", "must not contain an empty name")
		case slices.Contains(names, name):
			bad.add("include", "lists "+name+" more than once")
		case !s.canInclude(includable, name):
			bad.add("include", "cannot include "+name+s.includableHint(includable))
		default:
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return include{names: names}
}

func (s *InvestigationService) canInclude(includable map[string]resourceKind, name string) bool {
	kind, ok := includable[name]
	if !ok {
		return false
	}
	_, ok = s.loaders[kind]
	return ok
}

// includableHint lists the names that can be included here, for the error.
func (s *InvestigationService) includableHint(includable map[string]resourceKind) string {
	var names []string
	for name := range includable {
		if s.canInclude(includable, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "; nothing can be included here"
	}
	slices.Sort(names)
	return "; can include: " + strings.Join(names, ", ")
}

// included is a response's included object: link URL to the representation
// at that URL. An endpoint's response struct declares it as
//
//	Included included `json:"included,omitzero"`
//
// so a request without include has no included key (the map is nil), and a
// request with include always has one, even when it is empty ({}).
type included map[string]any

// loadIncluded builds the included object for items. It collects the
// requested links from every item, drops repeated URLs, calls each kind's
// loader once with the distinct IDs of all names of that kind, and returns a
// map from URL to representation. It must run inside s.read, so the included
// resources come from the same snapshot as the items.
//
// It returns nil when inc is empty, so the response has no included key.
// Included resources are never expanded further: depth is one level.
func (s *InvestigationService) loadIncluded(ctx context.Context, q *dbgen.Queries, items []itemLinks, inc include) (included, error) {
	if !inc.requested() {
		return nil, nil
	}

	// For each kind, the URL of each distinct target ID.
	targets := map[resourceKind]map[int64]string{}
	for _, item := range items {
		for _, name := range inc.names {
			r, ok := item[name]
			if !ok {
				continue // absent link: nothing to include for this item
			}
			if targets[r.Kind] == nil {
				targets[r.Kind] = map[int64]string{}
			}
			targets[r.Kind][r.ID] = r.URL
		}
	}

	out := included{}
	kinds := make([]resourceKind, 0, len(targets))
	for kind := range targets {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		load, ok := s.loaders[kind]
		if !ok {
			return nil, fmt.Errorf("include: no loader for %q", kind)
		}
		ids := make([]int64, 0, len(targets[kind]))
		for id := range targets[kind] {
			ids = append(ids, id)
		}
		slices.Sort(ids)

		reps, err := load(ctx, q, ids)
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", kind, err)
		}
		for _, id := range ids {
			rep, ok := reps[id]
			if !ok {
				return nil, fmt.Errorf("include %s: loader returned nothing for ID %d", kind, id)
			}
			out[targets[kind][id]] = rep
		}
	}
	return out, nil
}
