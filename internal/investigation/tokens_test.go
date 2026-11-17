package investigation

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestAcceptedTokens(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		next       string
		want       []string
		wantLog    string
		wantErrLog bool
	}{
		{"neither set", "", "", nil, "INVESTIGATION_API_TOKEN is not set", false},
		{"only next set", "", "next-secret", nil, "INVESTIGATION_API_TOKEN_NEXT is set but INVESTIGATION_API_TOKEN is not", true},
		{"only current set", "current-secret", "", []string{"current-secret"}, "", false},
		{"both set", "current-secret", "next-secret", []string{"current-secret", "next-secret"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			got := AcceptedTokens(tt.current, tt.next, logger)

			if !slices.Equal(got, tt.want) {
				t.Errorf("AcceptedTokens = %q, want %q", got, tt.want)
			}
			out := logs.String()
			if tt.wantLog == "" && out != "" {
				t.Errorf("unexpected log output:\n%s", out)
			}
			if tt.wantLog != "" && !strings.Contains(out, tt.wantLog) {
				t.Errorf("log output %q does not contain %q", out, tt.wantLog)
			}
			if tt.wantErrLog != strings.Contains(out, "level=ERROR") {
				t.Errorf("error log = %v, want %v:\n%s", !tt.wantErrLog, tt.wantErrLog, out)
			}
			for _, secret := range []string{"current-secret", "next-secret"} {
				if strings.Contains(out, secret) {
					t.Errorf("log output contains a token:\n%s", out)
				}
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	var nilService *InvestigationService
	if nilService.Enabled() {
		t.Error("nil service is enabled")
	}

	s, _ := newServiceWithTokens(t, nil, slog.Default())
	if s.Enabled() {
		t.Error("service with no tokens is enabled")
	}

	s, _ = newServiceWithTokens(t, []string{"", ""}, slog.Default())
	if s.Enabled() {
		t.Error("service with only empty tokens is enabled")
	}

	s, _ = newTestService(t)
	if !s.Enabled() {
		t.Error("service with a token is disabled")
	}
}
