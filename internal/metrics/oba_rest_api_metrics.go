package metrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

func FetchObaAPIMetrics(slugID string, serverBaseUrl string, apiKey string, client *http.Client) error {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	url := fmt.Sprintf("%s/api/where/metrics.json?key=%s", serverBaseUrl, apiKey)

	fmt.Printf("Fetching metrics from %s\n", url)

	resp, err := client.Get(url)
	if err != nil {
		err = fmt.Errorf("failed to fetch metrics from %s: %v", url, err)
		report.ReportErrorWithSentryOptions(err, report.SentryReportOptions{
			Tags: map[string]string{
				"slug_id": slugID,
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
		}
		wrappedErr = fmt.Errorf("unexpected status code from %s: %d", url, resp.StatusCode)
		report.ReportErrorWithSentryOptions(wrappedErr, report.SentryReportOptions{
			Tags: utils.MakeMap("slug_id", slugID),
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
			Tags: utils.MakeMap("slug_id", slugID),
			ExtraContext: map[string]interface{}{
				"url": url,
			},
		})
		return err
	}

	ObaApiStatus.WithLabelValues(slugID, url).Set(1)

	entry := metrics.Data.Entry

	ObaAgenciesWithCoverage.WithLabelValues(slugID).Set(float64(entry.AgenciesWithCoverageCount))

	for _, agencyID := range entry.AgencyIDs {
		if count, ok := entry.RealtimeRecordsTotal[agencyID]; ok {
			ObaRealtimeRecords.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if count, ok := entry.RealtimeTripCountsMatched[agencyID]; ok {
			ObaRealtimeTripsMatched.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if count, ok := entry.RealtimeTripCountsUnmatched[agencyID]; ok {
			ObaRealtimeTripsUnmatched.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if count, ok := entry.ScheduledTripsCount[agencyID]; ok {
			ObaScheduledTrips.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if count, ok := entry.StopIDsMatchedCount[agencyID]; ok {
			ObaStopsMatched.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if count, ok := entry.StopIDsUnmatchedCount[agencyID]; ok {
			ObaStopsUnmatched.WithLabelValues(slugID, agencyID).Set(float64(count))
		}

		if seconds, ok := entry.TimeSinceLastRealtimeUpdate[agencyID]; ok {
			ObaTimeSinceUpdate.WithLabelValues(slugID, agencyID).Set(float64(seconds))
		}
	}

	return nil
}
