package utils

import "testing"

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
