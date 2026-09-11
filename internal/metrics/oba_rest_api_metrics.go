package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
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

// fetchObaAPIMetrics retrieves and records metrics from the OneBusAway metrics
// API for a given agency on a given server and updates corresponding
// Prometheus metrics.
//
// It performs an HTTP GET request to the server's `/metrics.json` endpoint
// using the provided API key, decodes the response into structured fields, and
// populates per-agency Prometheus metrics (real-time and scheduled trip
// counts, stop match ratios, time-since-update, etc.).
//
// Server availability is *not* set here — that's the responsibility of the
// server-ping routine, which labels ObaApiStatus with (server_name, server_url)
// only. This function only emits per-agency metrics.
//
// In server-mode this function is called once per live agency per tick,
// but reuses the response cached by probeLiveAgencies instead of
// issuing another HTTP request. Agency-mode callers pass nil and fetch
// their own response.
//
// Parameters:
//   - agencyID: a string identifier used for metric labels and to look up the
//     API-reported counts.
//   - agencyName: the human-readable agency name, used as a metric label so
//     observers can identify the agency without decoding its ID.
//   - serverName: the human-readable server name, used as a metric label so
//     observers can group agencies by server.
//   - serverBaseUrl: the base URL of the OBA server (e.g., https://example.org).
//   - apiKey: the API key used to authenticate with the OBA server.
//   - client: the HTTP client used for the request. Required only when cacheMetrics
//     is nil, in which case it must be non-nil; injected (via MetricsService)
//     so requests flow through the instrumented transport.
//     A nil client is treated as a programming error.
//   - cacheMetrics: an already-decoded metrics response to use instead of fetching.
//     Pass nil to have the function fetch the endpoint itself.
// Returns:
//   - error: any error encountered during request or decoding. Nil when
//     cacheMetrics is supplied and the per-agency metrics are recorded.

func fetchObaAPIMetrics(ctx context.Context, agencyID, agencyName, serverName, serverBaseUrl, apiKey string, client *http.Client, staticStore *gtfs.StaticStore, logger *slog.Logger, unmatchedStopTracker *UnmatchedStopTracker, cacheMetrics *OBAMetrics) error {
	serverKey := models.ServerKey(serverBaseUrl, agencyID)
	serverURL := utils.SanitizeServerURL(serverBaseUrl)

	url := fmt.Sprintf("%s/api/where/metrics.json?key=%s", serverBaseUrl, apiKey)
	sanitizedURL := utils.SanitizeServerURL(url)
	// If cacheMetrics is nil, fetch the metrics from the OBA server. Otherwise, use the provided cacheMetrics data.
	if cacheMetrics == nil {
		if client == nil {
			err := fmt.Errorf("nil http client passed to fetchObaAPIMetrics for agency %s", agencyID)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{
					"agency_id":   agencyID,
					"server_name": serverName,
				},
			})
			return err
		}

		logger.Info("Fetching metrics from OBA server", "agency_id", agencyID, "server_name", serverName, "url", sanitizedURL)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("failed to create metrics request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			err = fmt.Errorf("failed to fetch metrics from %s: %v", sanitizedURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": agencyID, "server_name": serverName},
				ExtraContext: map[string]interface{}{
					"url": sanitizedURL,
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
				wrappedErr = fmt.Errorf("unexpected status code from %s: %d", sanitizedURL, resp.StatusCode)
			}
			report.ReportErrorWithSentryOptions(wrappedErr, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": agencyID, "server_name": serverName},
				ExtraContext: map[string]interface{}{
					"url":         sanitizedURL,
					"status_code": resp.StatusCode,
				},
			})
			return wrappedErr
		}

		var metrics OBAMetrics
		if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
			err = fmt.Errorf("failed to decode metrics from %s: %v", sanitizedURL, err)
			report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
				Tags: map[string]string{"agency_id": agencyID, "server_name": serverName},
				ExtraContext: map[string]interface{}{
					"url": sanitizedURL,
				},
			})
			return err
		}

		cacheMetrics = &metrics
	}

	if fetchTime, ok := staticStore.GetFetchTime(serverKey); ok {
		GtfsBundleLastFetchedTimestamp.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(fetchTime.Unix()))
	}

	entry := cacheMetrics.Data.Entry

	// The per-agency metrics below are only valid when the configured agencyID
	// is actually one of the agencies the OBA server reports in entry.AgencyIDs.
	// If it isn't, the server has no data for this agency, so report the
	// mismatch and skip every per-agency metric for this cycle.
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
			"agency_id", agencyID, "server_name", serverName, "url", sanitizedURL, "reported_agency_ids", entry.AgencyIDs)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{"agency_id": agencyID, "server_name": serverName},
			ExtraContext: map[string]interface{}{
				"url":                 sanitizedURL,
				"reported_agency_ids": entry.AgencyIDs,
			},
		})
		return nil
	}

	// Per-agency metrics below. Index the response maps with the configured
	// agencyID so every series is labeled with it, and only report values when
	// the server carries data for that agency.
	if count, ok := entry.RealtimeRecordsTotal[agencyID]; ok {
		ObaRealtimeRecords.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	if count, ok := entry.RealtimeTripCountsMatched[agencyID]; ok {
		ObaRealtimeTripsMatched.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	if count, ok := entry.RealtimeTripCountsUnmatched[agencyID]; ok {
		ObaRealtimeTripsUnmatched.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	matched := entry.RealtimeTripCountsMatched[agencyID]
	unmatched := entry.RealtimeTripCountsUnmatched[agencyID]
	total := matched + unmatched
	if total > 0 {
		TripMatchRatio.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(matched) / float64(total))
	}

	if count, ok := entry.ScheduledTripsCount[agencyID]; ok {
		ObaScheduledTrips.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	if count, ok := entry.StopIDsMatchedCount[agencyID]; ok {
		ObaStopsMatched.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	if count, ok := entry.StopIDsUnmatchedCount[agencyID]; ok {
		ObaStopsUnmatched.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(count))
	}

	stopMatched := entry.StopIDsMatchedCount[agencyID]
	stopUnmatched := entry.StopIDsUnmatchedCount[agencyID]
	stopTotal := stopMatched + stopUnmatched
	if stopTotal > 0 {
		StopMatchRatio.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(stopMatched) / float64(stopTotal))
	}

	if seconds, ok := entry.TimeSinceLastRealtimeUpdate[agencyID]; ok {
		ObaTimeSinceUpdate.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(seconds))
	}

	unmatchedStopIDs := entry.StopIDsUnmatched[agencyID]
	if len(unmatchedStopIDs) == 0 {
		ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(0)
		return nil
	}

	stopInfoMap, err := gtfs.GetStopLocationsByIDs(serverKey, unmatchedStopIDs, staticStore)
	if err != nil {
		ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(len(unmatchedStopIDs)))
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags:         map[string]string{"agency_id": agencyID, "server_name": serverName},
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
			serverName,
			serverURL,
			stopID,
			stop.Name,
			latStr,
			lonStr,
		).Set(1)
		resolved++
		unmatchedStopTracker.RecordLastSeen(serverKey, agencyID, agencyName, serverName, serverURL, stopID, stop.Name, latStr, lonStr)
	}

	unresolved := len(unmatchedStopIDs) - resolved
	ObaUnmatchedStopUnresolved.WithLabelValues(agencyID, agencyName, serverName, serverURL).Set(float64(unresolved))
	if unresolved > 0 {
		logger.Warn("OBA unmatched stop IDs could not be resolved against the local GTFS bundle",
			"agency_id", agencyID, "server_name", serverName, "requested", len(unmatchedStopIDs), "resolved", resolved)
	}
	reportUnmatchedStopClusters(serverKey, agencyID, agencyName, serverName, serverURL, stopInfoMap, unmatchedStopTracker)
	return nil
}
