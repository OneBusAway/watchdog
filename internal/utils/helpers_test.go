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
			expected: "https://api.example.com",
		},
		{
			name:     "strips userinfo",
			input:    "https://user:pass@rt.example.com/vehiclePositions.pb?token=SECRET",
			expected: "https://rt.example.com",
		},
		{
			name:     "strips path",
			input:    "https://rt.example.com/feed/positions.pb",
			expected: "https://rt.example.com",
		},
		{
			name:     "preserves port",
			input:    "https://rt.example.com:8080/path?key=x",
			expected: "https://rt.example.com:8080",
		},
		{
			name:     "preserves http scheme",
			input:    "http://example.com/foo",
			expected: "http://example.com",
		},
		{
			name:     "adds scheme when missing",
			input:    "example.com:8080/path",
			expected: "https://example.com:8080",
		},
		{
			name:     "protocol-relative",
			input:    "//example.com/path",
			expected: "https://example.com",
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
