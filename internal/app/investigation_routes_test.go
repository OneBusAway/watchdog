package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/config"
	"watchdog.onebusaway.org/internal/investigation"
)

// investigationProbePath is an Investigation API path from the design doc.
// No endpoint is implemented yet, so these tests also check Enabled(); once
// endpoints exist, a request to this path proves registration directly.
const investigationProbePath = "/v1/servers"

// newAppWithTokens builds an Application the way main does, from the two
// token environment values, and returns it with the log output of
// AcceptedTokens.
func newAppWithTokens(t *testing.T, current, next string) (*Application, string) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tokens := investigation.AcceptedTokens(current, next, logger)

	cfg := config.NewConfig(4000, "testing", nil)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := New(cfg, quiet, &http.Client{Timeout: 5 * time.Second}, "test", config.NewDroppedServersStore(), nil, tokens)
	return app, logs.String()
}

// getFromRoutes sends a GET with a bearer token through the full app router.
func getFromRoutes(t *testing.T, app *Application, path, token string) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Routes(ctx).ServeHTTP(rec, req)
	return rec.Code
}

func TestRoutes_DisabledWithoutToken(t *testing.T) {
	app, logs := newAppWithTokens(t, "", "")

	if app.InvestigationService.Enabled() {
		t.Error("Investigation API is enabled without INVESTIGATION_API_TOKEN")
	}
	if code := getFromRoutes(t, app, investigationProbePath, "anything"); code != http.StatusNotFound {
		t.Errorf("GET %s status = %d, want %d", investigationProbePath, code, http.StatusNotFound)
	}
	if !strings.Contains(logs, "Investigation API disabled: INVESTIGATION_API_TOKEN is not set") {
		t.Errorf("startup log does not say the API is disabled:\n%s", logs)
	}
}

func TestRoutes_NextWithoutCurrent(t *testing.T) {
	const next = "next-secret-value"
	app, logs := newAppWithTokens(t, "", next)

	if app.InvestigationService.Enabled() {
		t.Error("Investigation API is enabled with only INVESTIGATION_API_TOKEN_NEXT set")
	}
	if code := getFromRoutes(t, app, investigationProbePath, next); code != http.StatusNotFound {
		t.Errorf("GET %s status = %d, want %d", investigationProbePath, code, http.StatusNotFound)
	}
	if !strings.Contains(logs, "level=ERROR") {
		t.Errorf("expected an error log when only _NEXT is set:\n%s", logs)
	}
	if strings.Contains(logs, next) {
		t.Errorf("log output contains the token:\n%s", logs)
	}
}

func TestRoutes_EnabledWithToken(t *testing.T) {
	const current = "current-secret-value"
	app, logs := newAppWithTokens(t, current, "")

	if !app.InvestigationService.Enabled() {
		t.Error("Investigation API is disabled although INVESTIGATION_API_TOKEN is set")
	}
	if strings.Contains(logs, current) {
		t.Errorf("log output contains the token:\n%s", logs)
	}
}

// TestRoutes_WithoutInvestigationService covers Applications built without the
// service, as some tests do: Routes must not panic and the API stays off.
func TestRoutes_WithoutInvestigationService(t *testing.T) {
	app := newEmptyApplication(t)
	app.InvestigationService = nil

	if code := getFromRoutes(t, app, investigationProbePath, "anything"); code != http.StatusNotFound {
		t.Errorf("GET %s status = %d, want %d", investigationProbePath, code, http.StatusNotFound)
	}
}
