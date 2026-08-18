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

// metricsEndpoint is the OBA API metrics endpoint probed by fetchObaAPIMetrics.
const metricsEndpoint = "/api/where/metrics.json"

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
//   - Real-time and scheduled trip counts (matched/unmatched)
//   - Stop ID match/unmatch counts and ratios
//   - Trip and stop match ratios
//   - Time since last real-time update
//   - Locations of unmatched stops (if available)
//
// Parameters:
//   - agencyID: a string identifier used for metric labels and to look up the
//     API-reported counts.
//   - agencyName: the human-readable server/agency name, used as a metric label
//     so observers can identify the agency without decoding its ID.
//   - serverBaseUrl: the base URL of the OBA server (e.g., https://example.org).
//   - apiKey: the API key used to authenticate with the OBA server.
//   - client: an optional custom HTTP client; if nil, a default client with a timeout is used.
//
// Returns:
//   - error: any error encountered during request, decoding, or Prometheus reporting.

func fetchObaAPIMetrics(agencyID, agencyName, serverBaseUrl, apiKey string, client *http.Client, staticStore *gtfs.StaticStore, logger *slog.Logger, unmatchedStopTracker *UnmatchedStopTracker) error {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	url := fmt.Sprintf("%s/api/where/metrics.json?key=%s", serverBaseUrl, apiKey)
	sanitizedURL := utils.SanitizeServerURL(url)

	logger.Info("Fetching metrics from OBA server", "agency_id", agencyID, "url", sanitizedURL)

	resp, err := client.Get(url)
	if err != nil {
		err = fmt.Errorf("failed to fetch metrics from %s: %v", sanitizedURL, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"agency_id": agencyID,
			},
			ExtraContext: map[string]interface{}{
				"url": sanitizedURL,
			},
		})
		setObaApiMetricsStatus(agencyID, agencyName, serverBaseUrl, false)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var wrappedErr error
		if resp.StatusCode == http.StatusNotFound {
			wrappedErr = fmt.Errorf("server %s does not support metrics API", serverBaseUrl)
		} else {
			wrappedErr = fmt.Errorf("unexpected status code from %s: %d", sanitizedURL, resp.StatusCode)
		}
		report.ReportErrorWithSentryOptions(wrappedErr, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url":         sanitizedURL,
				"status_code": resp.StatusCode,
			},
		})

		setObaApiMetricsStatus(agencyID, agencyName, serverBaseUrl, false)
		return wrappedErr
	}

	var metrics OBAMetrics
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		err = fmt.Errorf("failed to decode metrics from %s: %v", sanitizedURL, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url": sanitizedURL,
			},
		})
		setObaApiMetricsStatus(agencyID, agencyName, serverBaseUrl, false)
		return err
	}
	setObaApiMetricsStatus(agencyID, agencyName, serverBaseUrl, true)

	if fetchTime, ok := staticStore.GetFetchTime(agencyID); ok {
		GtfsBundleLastFetchedTimestamp.WithLabelValues(agencyID, agencyName).Set(float64(fetchTime.Unix()))
	}

	entry := metrics.Data.Entry

	// The per-agency metrics below are only valid when the configured agencyID is
	// actually one of the agencies the OBA server reports in entry.AgencyIDs. If it
	// isn't, the server has no data for this agency, so report the mismatch and skip
	// every per-agency metric (RealtimeRecordsTotal onward) for this cycle.
	agencyFound := false
	for _, reportedAgencyID := range entry.AgencyIDs {
		if reportedAgencyID == agencyID {
			agencyFound = true
			break
		}
	}
	if !agencyFound {
		err := fmt.Errorf("configured agency %s not found in OBA metrics response for %s", agencyID, sanitizedURL)
		logger.Error("Configured agency not found in OBA metrics response",
			"agency_id", agencyID, "url", sanitizedURL, "reported_agency_ids", entry.AgencyIDs)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{
				"url":                 sanitizedURL,
				"reported_agency_ids": entry.AgencyIDs,
			},
		})
		return nil
	}

	// The per-agency maps are keyed by the agency IDs the server reports in
	// entry.AgencyIDs. Index them with the configured agencyID so every series is
	// labeled with it, and only report values when the server carries data for
	// that agency.
	if count, ok := entry.RealtimeRecordsTotal[agencyID]; ok {
		ObaRealtimeRecords.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	if count, ok := entry.RealtimeTripCountsMatched[agencyID]; ok {
		ObaRealtimeTripsMatched.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	if count, ok := entry.RealtimeTripCountsUnmatched[agencyID]; ok {
		ObaRealtimeTripsUnmatched.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	matched := entry.RealtimeTripCountsMatched[agencyID]
	unmatched := entry.RealtimeTripCountsUnmatched[agencyID]
	total := matched + unmatched
	if total > 0 {
		TripMatchRatio.WithLabelValues(agencyID, agencyName).Set(float64(matched) / float64(total))
	}

	if count, ok := entry.ScheduledTripsCount[agencyID]; ok {
		ObaScheduledTrips.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	if count, ok := entry.StopIDsMatchedCount[agencyID]; ok {
		ObaStopsMatched.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	if count, ok := entry.StopIDsUnmatchedCount[agencyID]; ok {
		ObaStopsUnmatched.WithLabelValues(agencyID, agencyName).Set(float64(count))
	}

	stopMatched := entry.StopIDsMatchedCount[agencyID]
	stopUnmatched := entry.StopIDsUnmatchedCount[agencyID]
	stopTotal := stopMatched + stopUnmatched
	if stopTotal > 0 {
		StopMatchRatio.WithLabelValues(agencyID, agencyName).Set(float64(stopMatched) / float64(stopTotal))
	}

	if seconds, ok := entry.TimeSinceLastRealtimeUpdate[agencyID]; ok {
		ObaTimeSinceUpdate.WithLabelValues(agencyID, agencyName).Set(float64(seconds))
	}

	unmatchedStopIDs := entry.StopIDsUnmatched[agencyID]
	if len(unmatchedStopIDs) == 0 {
		ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName).Set(0)
		return nil
	}

	stopInfoMap, err := gtfs.GetStopLocationsByIDs(agencyID, unmatchedStopIDs, staticStore)
	if err != nil {
		ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName).Set(float64(len(unmatchedStopIDs)))
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:         utils.MakeMap("agency_id", agencyID),
			ExtraContext: map[string]interface{}{"reason": "failed to match stop IDs to GTFS"},
		})
		return nil
	}

	resolved := 0
	for stopID, stop := range stopInfoMap {
		if stop.Latitude == nil || stop.Longitude == nil {
			continue
		}
		latStr := fmt.Sprintf("%.6f", *stop.Latitude)
		lonStr := fmt.Sprintf("%.6f", *stop.Longitude)
		ObaUnmatchedStopInfo.WithLabelValues(
			agencyID,
			agencyName,
			stopID,
			stop.Name,
			latStr,
			lonStr,
		).Set(1)
		resolved++
		unmatchedStopTracker.RecordLastSeen(agencyID, agencyName, stopID, stop.Name, latStr, lonStr)
	}

	unresolved := len(unmatchedStopIDs) - resolved
	ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName).Set(float64(unresolved))
	if unresolved > 0 {
		logger.Warn("OBA unmatched stop IDs could not be resolved against the local GTFS bundle",
			"agency_id", agencyID, "requested", len(unmatchedStopIDs), "resolved", resolved)
	}
	reportUnmatchedStopClusters(agencyID, agencyName, stopInfoMap, unmatchedStopTracker)
	return nil
}

// setObaApiMetricsStatus records the availability of a server's /metrics.json
// endpoint as a boolean gauge series. It is set to 1 on a successful request
// and to 0 on any failure so the series reflects failures instead of only
// successes.
func setObaApiMetricsStatus(agencyID, agencyName, serverBaseUrl string, up bool) {
	value := 0.0
	if up {
		value = 1
	}
	ObaApiStatus.WithLabelValues(agencyID, agencyName, utils.SanitizeServerURL(serverBaseUrl+metricsEndpoint)).Set(value)
}
