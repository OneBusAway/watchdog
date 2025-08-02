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

	for _, server := range integrationServers {
		srv := server
		t.Run(fmt.Sprintf("ServerID_%d", srv.ID), func(t *testing.T) {
			t.Parallel()

			staticStore := gtfs.NewStaticStore()
			err := gtfs.DownloadAndStoreGTFSBundle(srv.GtfsUrl, srv.ID, staticStore)
			if err != nil {
				t.Errorf("failed to download GTFS bundle for server %d : %v", srv.ID, err)
			} else {
				t.Logf("GTFS bundle downloaded successfully for server %d", srv.ID)
			}
		})
	}
}
