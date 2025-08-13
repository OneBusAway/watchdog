//go:build integration

package integration

import (
	"fmt"
	"testing"

	"watchdog.onebusaway.org/internal/gtfs"
)

// TestDownloadGTFSBundles verifies that GTFS bundles can be downloaded successfully
// for all configured servers. It runs a subtest for each server in parallel,
// and checks that the downloaded file is created without error.
func TestDownloadGTFSBundles(t *testing.T) {
	if len(integrationServers) == 0 {
		t.Skip("No servers found in config")
	}
	// This is sufficient for testing DownloadGTFSBundles functionality.
	// NewGtfsService requires a realtimeStore , boundingBoxStore, client and logger, 
	// but DownloadGTFSBundles does not use them, It only uses staticStore.
	// In the current test, we don't need to use them,
	// so we set them to nil. If future changes (e.g., in downloadAndStoreGTFSBundle) require them,
	// the test should be updated to use mock implementations.
	staticStore := gtfs.NewStaticStore()
	gtfsService := gtfs.NewGtfsService(staticStore,nil,nil,nil,nil)

	for _, server := range integrationServers {
		srv := server
		t.Run(fmt.Sprintf("ServerID_%d", srv.ID), func(t *testing.T) {
			t.Parallel()

			err := gtfsService.DownloadAndStoreGTFSBundle(srv.GtfsUrl, srv.ID)
			if err != nil {
				t.Errorf("failed to download GTFS bundle for server %d : %v", srv.ID, err)
			} else {
				t.Logf("GTFS bundle downloaded successfully for server %d", srv.ID)
			}
		})
	}
}
