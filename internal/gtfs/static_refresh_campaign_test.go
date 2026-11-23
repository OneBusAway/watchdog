package gtfs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func newFailingCampaignService(t *testing.T, onRequest func(*StaticStore)) (*GtfsService, models.ObaServer) {
	t.Helper()
	store := NewStaticStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		onRequest(store)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)
	server := models.ObaServer{ServerName: "OBA", AgencyID: "A", ObaBaseURL: ts.URL, GtfsStaticFeeds: []string{ts.URL + "/gtfs.zip"}}
	store.SetConfiguredServers([]models.ObaServer{server})
	service := NewGtfsService(store, NewRealtimeStore(), geo.NewBoundingBoxStore(), NewRouteAgencyIndex(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), ts.Client())
	return service, server
}

func TestStaticRefreshCampaignGivesUpWithinBudget(t *testing.T) {
	service, server := newFailingCampaignService(t, func(*StaticStore) {})
	var observations []StaticRefreshObservation
	service.SetStaticRefreshObserver(func(_ models.ObaServer, observation StaticRefreshObservation) {
		observations = append(observations, observation)
	})

	service.runStaticRefreshCampaign(context.Background(), server, StaticRefreshStartup, 1,
		staticRefreshRetryPolicy{initialDelay: 10 * time.Millisecond, maxDelay: 20 * time.Millisecond, budget: 250 * time.Millisecond})

	if len(observations) < 2 {
		t.Fatalf("expected retry and exhausted observations, got %+v", observations)
	}
	last := observations[len(observations)-1]
	if !last.GaveUp || last.Retrying {
		t.Fatalf("last observation did not exhaust the campaign: %+v", last)
	}
}

func TestStaticRefreshCampaignStopsRetryingRemovedServer(t *testing.T) {
	var requests atomic.Int32
	var service atomic.Pointer[GtfsService]
	removed := make(chan struct{})
	svc, server := newFailingCampaignService(t, func(store *StaticStore) {
		if requests.Add(1) == 1 {
			// The same two steps a config refresh performs.
			store.SetConfiguredServers(nil)
			service.Load().ReconcileStaticRefreshCampaigns(nil)
			close(removed)
		}
	})
	service.Store(svc)
	svc.refreshCoordinator.policy = staticRefreshRetryPolicy{initialDelay: time.Millisecond, maxDelay: time.Millisecond, budget: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !svc.refreshCoordinator.start(ctx, svc, server, StaticRefreshStartup, 1) {
		t.Fatal("campaign did not start")
	}
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("campaign never requested the feed")
	}
	// A campaign that kept retrying would make many requests at this delay.
	time.Sleep(50 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want one attempt before removal", got)
	}
}
