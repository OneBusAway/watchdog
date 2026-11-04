package investigation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cachePolicy is how long a client may reuse a response without asking again.
type cachePolicy struct {
	control      string
	lastModified time.Time // zero means no Last-Modified header
}

// cacheShort is for anything that can still change: open episodes,
// collections, clusters and counts. 30 seconds is one collection interval.
func cacheShort() cachePolicy {
	return cachePolicy{control: "private, max-age=30"}
}

// cacheImmutable is for records that no longer change: closed episodes, their
// static contexts and evidence, static versions, rule versions, bounding boxes
// and responses. lastModified is the episode's closed_at or the record's own
// time; pass the zero time to send no Last-Modified header.
func cacheImmutable(lastModified time.Time) cachePolicy {
	return cachePolicy{control: "private, max-age=86400", lastModified: lastModified}
}

// writeJSON writes body as the 200 response, or 304 Not Modified when the
// client's If-None-Match already names this body's ETag. If body cannot be
// encoded, the request fails with 500 instead.
func (s *InvestigationService) writeJSON(w http.ResponseWriter, r *http.Request, body any, cache cachePolicy) {
	data, err := encodeJSON(body)
	if err != nil {
		s.fail(w, r, fmt.Errorf("encode response: %w", err))
		return
	}
	writeEncoded(w, r, data, cache)
}

// encodeJSON encodes body without HTML escaping, so the & in a link's query
// string stays & instead of becoming &.
func encodeJSON(body any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeEncoded sends already encoded JSON with its validators and caching
// headers.
func writeEncoded(w http.ResponseWriter, r *http.Request, data []byte, cache cachePolicy) {
	h := w.Header()
	tag := etag(data)
	h.Set("ETag", tag)
	// middleware.SecurityHeaders sets no-store and Pragma: no-cache on every
	// response; replace both.
	h.Set("Cache-Control", cache.control)
	h.Del("Pragma")
	if !cache.lastModified.IsZero() {
		h.Set("Last-Modified", cache.lastModified.UTC().Format(http.TimeFormat))
	}

	if etagMatches(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	// net/http discards the body of a HEAD response, keeping the headers.
	_, _ = w.Write(data)
}

// etag is the first 32 hex characters (128 bits) of the body's SHA-256, in
// quotes as HTTP requires. Two different bodies sharing one is not a practical
// concern, and the header stays short.
func etag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

// etagMatches reports whether an If-None-Match header names tag. The header
// may be *, or a comma-separated list. If-None-Match uses weak comparison, so
// a W/ prefix is ignored.
func etagMatches(header, tag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == tag {
			return true
		}
	}
	return false
}
