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
