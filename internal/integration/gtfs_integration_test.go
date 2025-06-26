//go:build integration
package integration

import (
	"testing"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"watchdog.onebusaway.org/internal/gtfs"
)

// TestDownloadGTFSBundles verifies that GTFS bundles can be downloaded successfully
// for all configured servers. It runs a subtest for each server in parallel,
// and checks that the downloaded file is created without error.
func TestDownloadGTFSBundles(t *testing.T) {
	if len(integrationServers) == 0 {
		t.Skip("No servers found in config")
	}

	cacheDir := t.TempDir()

	for _, server := range integrationServers {
		srv := server
		t.Run(fmt.Sprintf("ServerID_%d", srv.ID), func(t *testing.T) {
			t.Parallel()

			hash := sha1.Sum([]byte(srv.GtfsUrl))
			hashStr := hex.EncodeToString(hash[:])
			cachePath := filepath.Join(cacheDir, fmt.Sprintf("server_%d_%s.zip", srv.ID, hashStr))

			_, err := gtfs.DownloadGTFSBundle(srv.GtfsUrl, cacheDir, srv.ID, hashStr)
			if err != nil {
				t.Errorf("Failed to download GTFS bundle for server %d at %s: %v", srv.ID, cachePath, err)
			} else {
				t.Logf("GTFS bundle downloaded successfully for server %d", srv.ID)
			}
		})
	}
}
