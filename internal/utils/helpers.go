package utils

import (
	"net/url"
	"strings"
)

// MakeMap creates and returns a map[string]string containing a single key-value pair.
func MakeMap(key, value string) map[string]string {
	return map[string]string{key: value}
}

// SanitizeServerURL reduces a URL to its scheme, host, and path, stripping any
// query string and userinfo. This is used for Prometheus label values so that
// credentials embedded in URLs (e.g. ?api_key=SECRET or user:pass@host) are
// never exposed through metrics, while still distinguishing different API
// endpoints by their path. It returns an empty string if the input cannot be
// parsed.
func SanitizeServerURL(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}

	var (
		parsed *url.URL
		err    error
	)
	switch {
	case strings.Contains(raw, "://"):
		parsed, err = url.Parse(raw)
	case strings.HasPrefix(raw, "//"):
		// Protocol-relative URL, e.g. //example.com
		parsed, err = url.Parse("https:" + raw)
	default:
		// No scheme; assume HTTPS, e.g. example.com:8080/path
		parsed, err = url.Parse("https://" + raw)
	}
	if err != nil {
		return ""
	}

	if parsed.Host == "" {
		return ""
	}

	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}
