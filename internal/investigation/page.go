package investigation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// page is one request's paging: how many items, and where the previous page
// stopped.
type page struct {
	// Size is the number of items on the page.
	Size int32
	// AfterID is the ID of the last item on the previous page, or nil for the
	// first page. Lists are ordered by ID descending, so the next page is the
	// items with an ID below it.
	AfterID *int64
}

// Limit is the LIMIT to query with: one more than Size, so the extra row shows
// whether another page exists without counting.
func (p page) Limit() int32 {
	return p.Size + 1
}

// parsePage reads page_size and cursor.
func parsePage(values url.Values, bad *invalidParams) page {
	p := page{Size: defaultPageSize}
	if raw, ok := present(values, "page_size", bad); ok {
		size, err := strconv.Atoi(raw)
		if err != nil || size < 1 || size > maxPageSize || strconv.Itoa(size) != raw {
			bad.add("page_size", "must be an integer from 1 to 200")
		} else {
			p.Size = int32(size)
		}
	}
	if raw, ok := present(values, "cursor", bad); ok {
		id, ok := decodeCursor(raw)
		if !ok {
			bad.add("cursor", "is not a valid cursor; follow links.next instead of building one")
		} else {
			p.AfterID = &id
		}
	}
	return p
}

// cursor is what a cursor holds. Clients treat it as opaque, so the format
// can change without breaking them.
type cursor struct {
	LastID int64 `json:"last_id"`
}

// encodeCursor is the base64url encoding, without padding, of
// {"last_id":lastID}. It is URL-safe, so it needs no escaping in a link.
func encodeCursor(lastID int64) string {
	data, _ := json.Marshal(cursor{LastID: lastID}) // cannot fail
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor reverses encodeCursor. Anything else, including a cursor with
// extra fields or a last_id that is not positive, is rejected.
func decodeCursor(raw string) (int64, bool) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c cursor
	if err := dec.Decode(&c); err != nil || dec.More() || c.LastID <= 0 {
		return 0, false
	}
	return c.LastID, true
}

// trimPage cuts the extra row fetched by page.Limit and reports whether there
// is another page.
func trimPage[T any](rows []T, p page) ([]T, bool) {
	if len(rows) > int(p.Size) {
		return rows[:p.Size], true
	}
	return rows, false
}

// pageLinks builds a page's self link and, when hasMore, its next link.
// lastID is the ID of the last item on this page. Both links keep the
// request's own parameters, so following next keeps the same filters; next
// only replaces the cursor.
func pageLinks(r *http.Request, lastID int64, hasMore bool) links {
	query := r.URL.Query()
	out := links{"self": linkWithQuery(r.URL.Path, query)}
	if hasMore {
		query.Set("cursor", encodeCursor(lastID))
		out["next"] = linkWithQuery(r.URL.Path, query)
	}
	return out
}
