package history

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	dto "github.com/prometheus/client_model/go"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
)

var testServer = models.ObaServer{
	ServerName: "Test Transit",
	ObaBaseURL: "https://api.test-transit.example/",
}

// failingBeginner stands in for the pool when the database is unreachable.
type failingBeginner struct{}

func (failingBeginner) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("connection refused")
}

func failureCount(t *testing.T, step string, tags writeTags) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.InvestigationHistoryWriteFailures.WithLabelValues(step, tags.serverName, tags.serverURL).Write(&m); err != nil {
		t.Fatalf("read failure counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestWrite_NilPoolNoop(t *testing.T) {
	h := NewHistoryService(nil, discardLogger(), "test")
	called := false
	h.write(context.Background(), "nil_pool_step", serverTags(testServer), func(*dbgen.Queries) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("write ran fn with a nil pool")
	}

	// A nil service is also safe, so callers never need a guard.
	var nilService *HistoryService
	nilService.write(context.Background(), "nil_pool_step", serverTags(testServer), func(*dbgen.Queries) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("write ran fn on a nil service")
	}
}

func TestWrite_ReportsErrors(t *testing.T) {
	const step = "reports_errors_step"
	tags := agencyTags(testServer, "1", "Agency One")
	logger, logs := bufferLogger()
	before := failureCount(t, step, tags)

	called := false
	write(context.Background(), failingBeginner{}, logger, step, tags, func(*dbgen.Queries) error {
		called = true
		return nil
	})

	if called {
		t.Fatal("fn ran although the transaction never began")
	}
	if got := failureCount(t, step, tags) - before; got != 1 {
		t.Fatalf("failure counter increased by %v, want 1", got)
	}
	out := logs.String()
	for _, want := range []string{"Failed to write investigation history", "step=" + step, `server_name="Test Transit"`, "agency_id=1", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}

func TestWrite_FailingFnRollsBack(t *testing.T) {
	pool := newMigratedPool(t)
	const step = "rolls_back_step"
	tags := serverTags(testServer)
	before := failureCount(t, step, tags)

	write(context.Background(), pool, discardLogger(), step, tags, func(q *dbgen.Queries) error {
		if _, err := ensureServer(context.Background(), q, testServer); err != nil {
			return err
		}
		return errors.New("later statement failed")
	})

	if got := failureCount(t, step, tags) - before; got != 1 {
		t.Fatalf("failure counter increased by %v, want 1", got)
	}
	if n := count(t, pool, "onebusaway_server"); n != 0 {
		t.Fatalf("onebusaway_server has %d rows after a failed write, want 0", n)
	}
}

func TestWrite_Commits(t *testing.T) {
	pool := newMigratedPool(t)
	h := NewHistoryService(pool, discardLogger(), "test")

	h.write(context.Background(), "commits_step", serverTags(testServer), func(q *dbgen.Queries) error {
		_, err := ensureServer(context.Background(), q, testServer)
		return err
	})

	if n := count(t, pool, "onebusaway_server"); n != 1 {
		t.Fatalf("onebusaway_server has %d rows, want 1", n)
	}
}

func TestWriteTags_SentryTags(t *testing.T) {
	server := serverTags(testServer).sentryTags()
	if len(server) != 1 || server["server_name"] != "Test Transit" {
		t.Errorf("server step tags = %v, want only server_name", server)
	}

	agency := agencyTags(testServer, "1", "Agency One").sentryTags()
	want := map[string]string{"server_name": "Test Transit", "agency_id": "1", "agency_name": "Agency One"}
	for k, v := range want {
		if agency[k] != v {
			t.Errorf("agency step tag %s = %q, want %q", k, agency[k], v)
		}
	}
	if _, ok := agency["server_url"]; ok {
		t.Error("Sentry tags must not carry the server URL")
	}
}
