package investigation

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

const nextToken = "next-token"

func TestAuthenticate_Accepts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, router := newServiceWithTokens(t, []string{testToken, nextToken}, logger)
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	tests := []struct {
		name   string
		header string
	}{
		{"current token", "Bearer " + testToken},
		{"next token", "Bearer " + nextToken},
		{"lowercase scheme", "bearer " + testToken},
		{"uppercase scheme", "BEARER " + nextToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			rec := doGet(t, router, "/v1/test", map[string]string{"Authorization": tt.header})

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !called {
				t.Error("handler did not run")
			}
		})
	}
}

func TestAuthenticate_Rejects(t *testing.T) {
	s, router := newTestService(t)
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{"no header", nil},
		{"wrong scheme", map[string]string{"Authorization": "Basic " + testToken}},
		{"wrong token", map[string]string{"Authorization": "Bearer not-the-token"}},
		{"token prefix", map[string]string{"Authorization": "Bearer test"}},
		{"bearer with empty token", map[string]string{"Authorization": "Bearer "}},
		{"bearer with no space", map[string]string{"Authorization": "Bearer"}},
		{"two spaces", map[string]string{"Authorization": "Bearer  " + testToken}},
		{"token without scheme", map[string]string{"Authorization": testToken}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, router, "/v1/test", tt.headers)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="watchdog-investigation"` {
				t.Errorf("WWW-Authenticate = %q", got)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
			var body problem
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Status != http.StatusUnauthorized || body.Title != "Unauthorized" {
				t.Errorf("body = %+v, want status 401 and title Unauthorized", body)
			}
		})
	}
	if called {
		t.Error("handler ran for a rejected request")
	}
}

func TestAuthenticate_DoesNotLogToken(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, router := newServiceWithTokens(t, []string{testToken, nextToken}, logger)
	called := false
	s.route(router, "/v1/test", okHandler(&called))

	const wrongToken = "wrong-secret-value"
	for _, header := range []string{
		"Bearer " + testToken,
		"Bearer " + nextToken,
		"Bearer " + wrongToken,
		"Basic " + wrongToken,
		"Bearer  " + testToken,
	} {
		doGet(t, router, "/v1/test", map[string]string{"Authorization": header})
	}

	if logs.Len() == 0 {
		t.Fatal("nothing was logged; the test needs the rejections to be logged to be meaningful")
	}
	for _, secret := range []string{testToken, nextToken, wrongToken} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log output contains %q:\n%s", secret, logs.String())
		}
	}
}

func TestCheckBearer_NoAcceptedTokens(t *testing.T) {
	if _, ok := checkBearer("Bearer anything", nil); ok {
		t.Error("checkBearer accepted a token with no accepted tokens")
	}
	if _, ok := checkBearer("Bearer ", [][]byte{{}}); ok {
		t.Error("checkBearer accepted an empty token")
	}
}
