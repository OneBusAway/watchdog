package config

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestConfigService_Wrappers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := http.DefaultClient
	cfg := &Config{}
	backoffStore := NewBackoffStore()
	droppedStore := NewDroppedServersStore()

	cs := NewConfigService(logger, client, cfg, backoffStore, droppedStore)
	if cs == nil {
		t.Fatalf("expected ConfigService, got nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel so refresh exits early

	cs.RefreshConfig(ctx, "http://invalid", "", "", time.Millisecond, 1, func([]models.ObaServer) {})

	_, _ = LoadConfigFromFile("invalid.json", logger, droppedStore)
	_, _ = LoadConfigFromURL(ctx, client, "http://invalid", "", "", 1, logger, droppedStore)

	// test success path by writing a temporary config file
	tmpFile, _ := os.CreateTemp("", "config-*.json")
	tmpFile.Write([]byte(`{}`))
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, _ = LoadConfigFromFile(tmpFile.Name(), logger, droppedStore)
}
