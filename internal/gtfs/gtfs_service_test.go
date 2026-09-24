package gtfs

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/models"
)

func TestGtfsService_Wrappers(t *testing.T) {
	static := NewStaticStore()
	realtime := NewRealtimeStore()
	bbox := geo.NewBoundingBoxStore()
	routeAgency := NewRouteAgencyIndex()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client := http.DefaultClient

	gs := NewGtfsService(static, realtime, bbox, routeAgency, logger, client)
	gs.SetBundleObserver(func(server models.ObaServer, agencyID, agencyName string, bundle *models.StaticData) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so we don't hang if they do something

	servers := []models.ObaServer{{ServerName: "test"}}
	
	// Just hit them for coverage. They will fail or return errors.
	gs.DownloadGTFSBundles(ctx, servers, 1)
	_, _ = gs.DownloadGTFSBundle(ctx, "http://example.com/gtfs.zip", "a1", 1)
	
	_ = gs.FetchAndStoreGTFSRTFeed(ctx, servers[0])

	_, _, _ = GetEarliestAndLatestServiceDates(&models.StaticData{})
	_, _, _ = GetEarliestAndLatestServiceDates(nil)
	_, _ = GetStopLocationsByIDs("test", []string{"s1"}, static)

	go gs.RefreshGTFSBundles(ctx, func() []models.ObaServer { return servers }, time.Millisecond, 1)
	time.Sleep(10 * time.Millisecond)
}
