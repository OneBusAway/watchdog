package metrics

import (
	"context"
	"fmt"

	onebusaway "github.com/OneBusAway/go-sdk"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// currentTimeEndpoint is the OBA API endpoint pinged by serverPing to verify
// server availability.
const currentTimeEndpoint = "/api/where/current-time.json"

// serverPing pings the `/current-time` endpoint of the given OneBusAway server
// to verify the API is reachable and returning valid data.
//
// The ping is server-wide (the endpoint takes no agency parameter), so the
// ObaApiStatus gauge is labeled with server identity only. Errors are reported
// to Sentry with server context.
//
// Parameters:
//   - client: the shared OneBusAway SDK client for the server, injected by
//     the caller so the client's connection pool and instrumentation are
//     reused across collection ticks.
//   - server: a models.ObaServer object containing the base URL, API key,
//     and server name.
//
// Returns:
//   - bool: true when the server returned a readable time; the caller uses
//     this to gate subsequent steps (any non-true value aborts the per-server
//     collection cycle and triggers a backoff update).
func serverPing(client *onebusaway.Client, server models.ObaServer) bool {
	ctx := context.Background()
	response, err := client.CurrentTime.Get(ctx)

	serverURL := utils.SanitizeServerURL(server.ObaBaseURL + currentTimeEndpoint)

	if err != nil {
		err := fmt.Errorf("failed to ping OBA server %s: %v", server.ObaBaseURL, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_name", server.ServerName),
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
		})
		ObaApiStatus.WithLabelValues(server.ServerName, serverURL).Set(0)
		return false
	}

	if response.Data.Entry.ReadableTime != "" {
		ObaApiStatus.WithLabelValues(server.ServerName, serverURL).Set(1)
		return true
	}
	ObaApiStatus.WithLabelValues(server.ServerName, serverURL).Set(0)
	return false
}
