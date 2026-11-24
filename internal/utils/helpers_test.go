package utils

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeServerURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "strips query string",
			input:    "https://api.example.com/api/where/metrics.json?key=SECRET",
			expected: "https://api.example.com/api/where/metrics.json",
		},
		{
			name:     "strips userinfo",
			input:    "https://user:pass@rt.example.com/vehiclePositions.pb?token=SECRET",
			expected: "https://rt.example.com/vehiclePositions.pb",
		},
		{
			name:     "lowercases scheme and host but not path",
			input:    "HTTPS://user:pw@API.Example.COM/Where/API?key=x",
			expected: "https://api.example.com/Where/API",
		},
		{
			name:     "keeps path",
			input:    "https://rt.example.com/feed/positions.pb",
			expected: "https://rt.example.com/feed/positions.pb",
		},
		{
			name:     "preserves port",
			input:    "https://rt.example.com:8080/path?key=x",
			expected: "https://rt.example.com:8080/path",
		},
		{
			name:     "preserves http scheme",
			input:    "http://example.com/foo",
			expected: "http://example.com/foo",
		},
		{
			name:     "adds scheme when missing",
			input:    "example.com:8080/path",
			expected: "https://example.com:8080/path",
		},
		{
			name:     "protocol-relative",
			input:    "//example.com/path",
			expected: "https://example.com/path",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace input",
			input:    "   ",
			expected: "",
		},
		{
			name:     "malformed input",
			input:    "Value of VehiclePositionUrl",
			expected: "",
		},
		{
			name:     "invalid characters",
			input:    "https://example .com/feed",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeServerURL(tt.input); got != tt.expected {
				t.Fatalf("SanitizeServerURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeErrorURL(t *testing.T) {
	rawURL := "https://user:password@example.com/feed?api_key=super-secret"
	err := errors.New("Get \"" + rawURL + "\": dial tcp: connection refused")
	got := SanitizeErrorURL(err, rawURL)
	if got == nil {
		t.Fatal("expected sanitized error")
	}
	if strings.Contains(got.Error(), "password") || strings.Contains(got.Error(), "super-secret") || strings.Contains(got.Error(), "user:") {
		t.Fatalf("sanitized error leaked URL credentials: %v", got)
	}
	if !strings.Contains(got.Error(), "https://example.com/feed") {
		t.Fatalf("sanitized error should retain safe endpoint: %v", got)
	}

	apiKeyError := errors.New("request failed: https://example.com/api?key=separate-api-key")
	got = SanitizeErrorURL(apiKeyError, "https://example.com", "separate-api-key")
	if strings.Contains(got.Error(), "separate-api-key") {
		t.Fatalf("sanitized error leaked separately configured API key: %v", got)
	}
}

func TestSanitizeErrorURLRedactsHTTPErrorURLAfterUserinfoMasking(t *testing.T) {
	err := errors.New(`Get "http://url-user:***@127.0.0.1:8080/config.json?api_key=query-secret": dial tcp: connection refused`)
	got := SanitizeErrorURL(err, "http://url-user:url-password@127.0.0.1:8080/config.json?api_key=query-secret")
	if got == nil {
		t.Fatal("expected sanitized error")
	}
	for _, secret := range []string{"url-user", "query-secret", "api_key="} {
		if strings.Contains(got.Error(), secret) {
			t.Errorf("sanitized error leaked %q: %v", secret, got)
		}
	}
	if !strings.Contains(got.Error(), "http://127.0.0.1:8080/config.json") {
		t.Errorf("sanitized error should retain the safe endpoint: %v", got)
	}
}

func TestSanitizeErrorURLReplacesConfiguredURLBeforeScanningEmbeddedURLs(t *testing.T) {
	const (
		username    = "apostrophe-user"
		password    = "apostrophe-password"
		querySecret = "apostrophe-query-secret"
	)

	rawURL := "https://" + username + ":" + password +
		"@feed.example.com/static.zip?label=O'Reilly&api_key=" + querySecret

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse configured URL: %v", err)
	}

	maskedAsteriskURL := *parsed
	maskedAsteriskURL.User = url.UserPassword(username, "***")

	safeURL := "https://feed.example.com/static.zip"

	tests := []struct {
		name string
		url  string
	}{
		{name: "configured URL", url: rawURL},
		{name: "URL redacted password", url: parsed.Redacted()},
		{name: "asterisk-masked password", url: maskedAsteriskURL.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(`GET "` + tt.url + `": synthetic fetch failure`)
			got := SanitizeErrorURL(err, rawURL)

			if got == nil {
				t.Fatal("expected sanitized error")
			}

			for _, secret := range []string{
				username,
				password,
				querySecret,
				"api_key=",
			} {
				if strings.Contains(got.Error(), secret) {
					t.Errorf("sanitized error leaked %q: %v", secret, got)
				}
			}

			if !strings.Contains(got.Error(), safeURL) {
				t.Errorf("safe endpoint %q missing: %v", safeURL, got)
			}
		})
	}
}

func TestSanitizeErrorURLRedactsAppendedQueryAndRedirectApostrophe(t *testing.T) {
	const (
		configuredSecret = "configured-query-secret"
		appendedSecret   = "server-appended-secret"
		redirectSecret   = "redirect-query-secret"
	)

	configuredURL := "https://configured.example/feed.zip?api_key=" + configuredSecret

	tests := []struct {
		name     string
		errorURL string
		safeURL  string
		secrets  []string
	}{
		{
			name: "server-appended query parameters",
			errorURL: configuredURL +
				"&server_key=" + appendedSecret,
			safeURL: "https://configured.example/feed.zip",
			secrets: []string{configuredSecret, appendedSecret, "server_key="},
		},
		{
			name: "redirect URL with apostrophe",
			errorURL: "https://redirect.example/feed?label=O'Reilly&api_key=" +
				redirectSecret,
			safeURL: "https://redirect.example/feed",
			secrets: []string{configuredSecret, redirectSecret, "api_key="},
		},
		{
			name: "configured URL is a prefix of a longer query value",
			errorURL: "https://configured.example/feed.zip?api_key=" +
				configuredSecret + "-v2&server_key=" + appendedSecret,
			safeURL: "https://configured.example/feed.zip",
			secrets: []string{configuredSecret, appendedSecret, "server_key="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(`GET "` + tt.errorURL + `": synthetic redirect failure`)
			got := SanitizeErrorURL(err, configuredURL)

			if got == nil {
				t.Fatal("expected sanitized error")
			}

			for _, secret := range tt.secrets {
				if strings.Contains(got.Error(), secret) {
					t.Errorf("sanitized error leaked %q: %v", secret, got)
				}
			}

			if !strings.Contains(got.Error(), tt.safeURL) {
				t.Errorf("safe endpoint %q missing: %v", tt.safeURL, got)
			}
		})
	}
}
