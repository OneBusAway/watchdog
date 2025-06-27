//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
)

// TestOBAConnection verifies that the OBA API is reachable and responds with valid
// current time data for all configured servers. It runs a subtest for each server
// in parallel, using a context with timeout to avoid hanging on unresponsive servers.
func TestOBAConnection(t *testing.T) {
	if len(integrationServers) == 0 {
		t.Skip("No servers found in config")
	}

	for _, server := range integrationServers {
		srv := server
		t.Run(fmt.Sprintf("ServerID_%d", srv.ID), func(t *testing.T) {
			t.Parallel()

			if srv.ObaApiKey == "" || srv.ObaBaseURL == "" {
				t.Skipf("Skipping server ID %d: missing API key or BaseURL", srv.ID)
			}

			client := onebusaway.NewClient(
				option.WithAPIKey(srv.ObaApiKey),
				option.WithBaseURL(srv.ObaBaseURL),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.CurrentTime.Get(ctx)
			if err != nil {
				t.Errorf("Server ID %d (%s): Failed to connect to OBA API: %v", srv.ID, srv.ObaBaseURL, err)
				return
			}

			if resp.Data.Entry.ReadableTime == "" {
				t.Errorf("Server ID %d (%s): Expected non-empty ReadableTime from OBA API", srv.ID, srv.ObaBaseURL)
			} else {
				t.Logf("Server ID %d (%s): Successfully retrieved current time: %s",
					srv.ID, srv.ObaBaseURL, resp.Data.Entry.ReadableTime)
			}
		})
	}
}
