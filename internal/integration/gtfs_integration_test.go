//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
)

// TestDownloadGTFSBundles verifies that GTFS bundles can be downloaded successfully
// for all configured servers. It runs a subtest for each server in parallel,
// and checks that the downloaded file is created without error.
func TestDownloadGTFSBundles(t *testing.T) {
	if len(integrationServers) == 0 {
		t.Skip("No servers found in config")
	}

	staticStore := gtfs.NewStaticStore()
	realtimeStore := gtfs.NewRealtimeStore()
	boundingBoxStore := geo.NewBoundingBoxStore()
	logger := slog.Default()
	client := &http.Client{}
	gtfsService := gtfs.NewGtfsService(staticStore, realtimeStore, boundingBoxStore, logger, client)
	ctx := context.Background()
	for _, server := range integrationServers {
		srv := server
		t.Run(fmt.Sprintf("Agency_%s", srv.AgencyID), func(t *testing.T) {
			t.Parallel()
			for _, gtfsURL := range srv.GtfsStaticFeeds {
				staticBundle, err := gtfsService.DownloadGTFSBundle(ctx, gtfsURL, srv.AgencyID, 20)
				if err != nil {
					t.Errorf("failed to download GTFS bundle for agency %s : %v", srv.AgencyID, err)
					return
				}
				err = gtfsService.StoreGTFSBundle(staticBundle, srv.ServerKey())
				if err != nil {
					t.Errorf("failed to store GTFS bundle for agency %s : %v", srv.AgencyID, err)
					return
				}
			}
			t.Logf("GTFS bundle downloaded successfully for agency %s", srv.AgencyID)
		})
	}
}
