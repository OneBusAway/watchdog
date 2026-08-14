package metrics

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/report"
	"watchdog.onebusaway.org/internal/utils"
)

type OBAMetrics struct {
	Code        int    `json:"code"`
	CurrentTime int64  `json:"currentTime"`
	Text        string `json:"text"`
	Version     int    `json:"version"`
	Data        struct {
		Entry struct {
			AgenciesWithCoverageCount   int                 `json:"agenciesWithCoverageCount"`
			AgencyIDs                   []string            `json:"agencyIDs"`
			RealtimeRecordsTotal        map[string]int      `json:"realtimeRecordsTotal"`
			RealtimeTripCountsMatched   map[string]int      `json:"realtimeTripCountsMatched"`
			RealtimeTripCountsUnmatched map[string]int      `json:"realtimeTripCountsUnmatched"`
			RealtimeTripIDsUnmatched    map[string][]string `json:"realtimeTripIDsUnmatched"`
			ScheduledTripsCount         map[string]int      `json:"scheduledTripsCount"`
			StopIDsMatchedCount         map[string]int      `json:"stopIDsMatchedCount"`
			StopIDsUnmatched            map[string][]string `json:"stopIDsUnmatched"`
			StopIDsUnmatchedCount       map[string]int      `json:"stopIDsUnmatchedCount"`
			TimeSinceLastRealtimeUpdate map[string]int      `json:"timeSinceLastRealtimeUpdate"`
		} `json:"entry"`
	} `json:"data"`
}

// fetchObaAPIMetrics retrieves and records metrics from the OneBusAway metrics API
// for a given server and updates corresponding Prometheus metrics.
//
// It performs an HTTP GET request to the server's `/metrics.json` endpoint using the provided
// API key, decodes the response into structured fields, and populates Prometheus metrics such as:
//
//   - Number of agencies with coverage
//   - Real-time and scheduled trip counts (matched/unmatched)
//   - Stop ID match/unmatch counts and ratios
//   - Trip and stop match ratios
//   - Time since last real-time update
//   - Locations of unmatched stops (if available)
//
// Parameters:
//   - agencyID: a string identifier used for metric labels and to look up the
//     API-reported counts.
//   - serverBaseUrl: the base URL of the OBA server (e.g., https://example.org).
//   - apiKey: the API key used to authenticate with the OBA server.
//   - client: an optional custom HTTP client; if nil, a default client with a timeout is used.
//
// Returns:
//   - error: any error encountered during request, decoding, or Prometheus reporting.

func fetchObaAPIMetrics(agencyID, serverBaseUrl, apiKey string, client *http.Client, staticStore *gtfs.StaticStore, logger *slog.Logger, unmatchedStopTracker *UnmatchedStopTracker) error {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	url := fmt.Sprintf("%s/api/where/metrics.json?key=%s", serverBaseUrl, apiKey)

	logger.Info("Fetching metrics from OBA server", "agency_id", agencyID, "url", url)

	resp, err := client.Get(url)
	if err != nil {
		err = fmt.Errorf("failed to fetch metrics from %s: %v", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id": agencyID,
			},
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var wrappedErr error
		if resp.StatusCode == http.StatusNotFound {
			wrappedErr = fmt.Errorf("server %s does not support metrics API", serverBaseUrl)
		} else {
			wrappedErr = fmt.Errorf("unexpected status code from %s: %d", url, resp.StatusCode)
		}
		report.ReportErrorWithSentryOptions(wrappedErr, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url":         url,
				"status_code": resp.StatusCode,
			},
		})

		return wrappedErr
	}

	var metrics OBAMetrics
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		err = fmt.Errorf("failed to decode metrics from %s: %v", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return err
	}
	ObaApiStatus.WithLabelValues(agencyID, utils.SanitizeServerURL(serverBaseUrl)).Set(1)

	if fetchTime, ok := staticStore.GetFetchTime(agencyID); ok {
		GtfsBundleLastFetchedTimestamp.WithLabelValues(agencyID).Set(float64(fetchTime.Unix()))
	}

	entry := metrics.Data.Entry

	ObaAgenciesWithCoverage.WithLabelValues(agencyID).Set(float64(entry.AgenciesWithCoverageCount))

	// The OBA metrics API reports per-agency statistics, so iterate over every
	// agency the server covers and label each series with that agency's ID.
	for _, reportedAgencyID := range entry.AgencyIDs {
		if count, ok := entry.RealtimeRecordsTotal[reportedAgencyID]; ok {
			ObaRealtimeRecords.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		if count, ok := entry.RealtimeTripCountsMatched[reportedAgencyID]; ok {
			ObaRealtimeTripsMatched.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		if count, ok := entry.RealtimeTripCountsUnmatched[reportedAgencyID]; ok {
			ObaRealtimeTripsUnmatched.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		matched := entry.RealtimeTripCountsMatched[reportedAgencyID]
		unmatched := entry.RealtimeTripCountsUnmatched[reportedAgencyID]
		total := matched + unmatched
		if total > 0 {
			TripMatchRatio.WithLabelValues(reportedAgencyID).Set(float64(matched) / float64(total))
		}

		if count, ok := entry.ScheduledTripsCount[reportedAgencyID]; ok {
			ObaScheduledTrips.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		if count, ok := entry.StopIDsMatchedCount[reportedAgencyID]; ok {
			ObaStopsMatched.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		if count, ok := entry.StopIDsUnmatchedCount[reportedAgencyID]; ok {
			ObaStopsUnmatched.WithLabelValues(reportedAgencyID).Set(float64(count))
		}

		stopMatched := entry.StopIDsMatchedCount[reportedAgencyID]
		stopUnmatched := entry.StopIDsUnmatchedCount[reportedAgencyID]
		stopTotal := stopMatched + stopUnmatched
		if stopTotal > 0 {
			StopMatchRatio.WithLabelValues(reportedAgencyID).Set(float64(stopMatched) / float64(stopTotal))
		}

		if seconds, ok := entry.TimeSinceLastRealtimeUpdate[reportedAgencyID]; ok {
			ObaTimeSinceUpdate.WithLabelValues(reportedAgencyID).Set(float64(seconds))
		}

		unmatchedStopIDs := entry.StopIDsUnmatched[reportedAgencyID]
		if len(unmatchedStopIDs) == 0 {
			ObaUnmatchedStopUnresolved.WithLabelValues(reportedAgencyID).Set(0)
			continue
		}

		stopInfoMap, err := gtfs.GetStopLocationsByIDs(agencyID, unmatchedStopIDs, staticStore)
		if err != nil {
			ObaUnmatchedStopUnresolved.WithLabelValues(reportedAgencyID).Set(float64(len(unmatchedStopIDs)))
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags:         utils.MakeMap("agency_id", agencyID),
				ExtraContext: map[string]interface{}{"reason": "failed to match stop IDs to GTFS"},
			})
			continue
		}

		resolved := 0
		for stopID, stop := range stopInfoMap {
			if stop.Latitude == nil || stop.Longitude == nil {
				continue
			}
			latStr := fmt.Sprintf("%.6f", *stop.Latitude)
			lonStr := fmt.Sprintf("%.6f", *stop.Longitude)
			ObaUnmatchedStopInfo.WithLabelValues(
				reportedAgencyID,
				stopID,
				stop.Name,
				latStr,
				lonStr,
			).Set(1)
			resolved++
			unmatchedStopTracker.RecordLastSeen(reportedAgencyID, stopID, stop.Name, latStr, lonStr)
		}

		unresolved := len(unmatchedStopIDs) - resolved
		ObaUnmatchedStopUnresolved.WithLabelValues(reportedAgencyID).Set(float64(unresolved))
		if unresolved > 0 {
			logger.Warn("OBA unmatched stop IDs could not be resolved against the local GTFS bundle",
				"agency_id", reportedAgencyID, "requested", len(unmatchedStopIDs), "resolved", resolved)
		}
		reportUnmatchedStopClusters(reportedAgencyID, stopInfoMap, unmatchedStopTracker)
	}
	return nil
}
