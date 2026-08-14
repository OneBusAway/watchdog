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
		t.Run(fmt.Sprintf("Agency_%s", srv.AgencyID), func(t *testing.T) {
			t.Parallel()

			if srv.ObaApiKey == "" || srv.ObaBaseURL == "" {
				t.Skipf("Skipping agency %s: missing API key or BaseURL", srv.AgencyID)
			}

			client := onebusaway.NewClient(
				option.WithAPIKey(srv.ObaApiKey),
				option.WithBaseURL(srv.ObaBaseURL),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.CurrentTime.Get(ctx)
			if err != nil {
				t.Errorf("Agency %s (%s): Failed to connect to OBA API: %v", srv.AgencyID, srv.ObaBaseURL, err)
				return
			}

			if resp.Data.Entry.ReadableTime == "" {
				t.Errorf("Agency %s (%s): Expected non-empty ReadableTime from OBA API", srv.AgencyID, srv.ObaBaseURL)
			} else {
				t.Logf("Agency %s (%s): Successfully retrieved current time: %s",
					srv.AgencyID, srv.ObaBaseURL, resp.Data.Entry.ReadableTime)
			}
		})
	}
}
