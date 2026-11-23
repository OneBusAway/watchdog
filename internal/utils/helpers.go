package utils

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// embeddedHTTPURLPattern finds HTTP(S) URLs inside error strings, including the
// URL formatting used by net/http when it masks URL passwords.
var embeddedHTTPURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

// MakeMap creates and returns a map[string]string containing a single key-value pair.
func MakeMap(key, value string) map[string]string {
	return map[string]string{key: value}
}

// SanitizeErrorURL removes a configured URL from an error message before the
// error is returned, logged, or sent to an external error reporter. The original
// error is deliberately not wrapped: its chain may contain a url.Error whose
// URL field still includes credentials.
func SanitizeErrorURL(err error, rawURL string, secrets ...string) error {
	if err == nil {
		return nil
	}
	safeURL := SanitizeServerURL(rawURL)
	message := err.Error()
	if rawURL != "" {
		// Replace the complete configured URL before scanning embedded URLs.
		// Otherwise, a URL containing an apostrophe can be partially matched
		// by embeddedHTTPURLPattern, leaving the remainder of its query intact.
		message = strings.ReplaceAll(message, rawURL, safeURL)
		if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
			message = strings.ReplaceAll(message, parsed.String(), safeURL)
			// net/http masks URL passwords in url.Error using URL.Redacted().
			message = strings.ReplaceAll(message, parsed.Redacted(), safeURL)

			if parsed.User != nil {
				if _, hasPassword := parsed.User.Password(); hasPassword {
					masked := *parsed
					masked.User = url.UserPassword(parsed.User.Username(), "***")
					message = strings.ReplaceAll(message, masked.String(), safeURL)
				}
			}
		}
	}
	message = embeddedHTTPURLPattern.ReplaceAllStringFunc(message, func(candidate string) string {
		safe := SanitizeServerURL(candidate)
		if safe == "" {
			return "[REDACTED URL]"
		}
		return safe
	})
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
		message = strings.ReplaceAll(message, url.QueryEscape(secret), "[REDACTED]")
	}
	return errors.New(message)
}

// SanitizeServerURL reduces a URL to its scheme, host, and path, stripping any
// query string and userinfo. This is used for Prometheus label values so that
// credentials embedded in URLs (e.g. ?api_key=SECRET or user:pass@host) are
// never exposed through metrics, while still distinguishing different API
// endpoints by their path. The host is lowercased, because host names are
// case-insensitive and one server must not appear under two label values or
// identities; the path keeps its case, because paths are case-sensitive. It
// returns an empty string if the input cannot be parsed.
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

	return parsed.Scheme + "://" + strings.ToLower(parsed.Host) + parsed.Path
}
