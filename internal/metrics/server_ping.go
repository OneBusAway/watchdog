package metrics

import (
	"context"
	"fmt"
	"strconv"

	onebusaway "github.com/OneBusAway/go-sdk"
	"github.com/OneBusAway/go-sdk/option"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

// ServerPing pings the `/current-time` endpoint of the given OneBusAway server
// to verify the API is reachable and returning valid data.
//
// If the request is successful and the response contains a valid readable time,
// the `ObaApiStatus` Prometheus metric is set to 1 for the server. Otherwise, it is set to 0.
// Errors (such as failed requests or invalid responses) are reported to Sentry with server context.
//
// Parameters:
//   - server: a models.ObaServer object containing the base URL, API key, and server ID.
//
// Returns:
//   - None (side effects include reporting to Prometheus and Sentry).
func ServerPing(server models.ObaServer) {
	client := onebusaway.NewClient(
		option.WithAPIKey(server.ObaApiKey),
		option.WithBaseURL(server.ObaBaseURL),
	)

	ctx := context.Background()
	response, err := client.CurrentTime.Get(ctx)

	if err != nil {
		err := fmt.Errorf("failed to ping OBA server %s: %v", server.ObaBaseURL, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("server_id", strconv.Itoa(server.ID)),
			ExtraContext: map[string]interface{}{
				"oba_base_url": server.ObaBaseURL,
			},
		})
		// Update status metric
		ObaApiStatus.WithLabelValues(
			strconv.Itoa(server.ID),
			server.ObaBaseURL,
		).Set(0)
		return
	}

	// Check response validity
	if response.Data.Entry.ReadableTime != "" {
		ObaApiStatus.WithLabelValues(
			strconv.Itoa(server.ID),
			server.ObaBaseURL,
		).Set(1)
	} else {
		ObaApiStatus.WithLabelValues(
			strconv.Itoa(server.ID),
			server.ObaBaseURL,
		).Set(0)
	}
}
