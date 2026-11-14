package investigation

import (
	"net/url"
	"strings"
)

// links is a representation's links object: link name to absolute path. JSON
// encodes map keys in sorted order, so the same links always give the same
// body and ETag. A link whose target does not exist is left out, not null.
type links map[string]string

// linkWithQuery builds path with params as its query string. It is the one way
// links with filters are built (page links, links to filtered lists), so the
// same filters always give exactly the same URL. That matters twice: included
// resources are keyed by URL, and the ETag hashes the body.
//
// Parameters are sorted by name. Values are escaped, except ":" and ",",
// which are allowed in a query and keep times and include lists readable:
// opened_from=2026-10-23T00:00:00Z rather than 2026-10-23T00%3A00%3A00Z. "+",
// as in a +02:00 offset, stays escaped as %2B.
func linkWithQuery(path string, params url.Values) string {
	query := params.Encode()
	if query == "" {
		return path
	}
	query = strings.NewReplacer("%3A", ":", "%2C", ",").Replace(query)
	return path + "?" + query
}
