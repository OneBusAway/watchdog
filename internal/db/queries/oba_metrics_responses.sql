-- name: ListOBAMetricsResponsesWithAgencies :many
-- Headers and agency counts for the given responses in one read. A response with no
-- agency rows still returns one row, with NULL agency columns. A response that does
-- not exist returns no rows. Used by the E20 page and its include loader.
SELECT r.response_id, r.server_id, r.observed_at, r.upstream_current_time, r.api_version,
       a.agency_id, a.response_complete, a.unmatched_stop_count, a.unmatched_trip_count,
       a.matched_stop_count, a.matched_trip_count
FROM onebusaway_metrics_response AS r
LEFT JOIN onebusaway_metrics_response_agency AS a ON a.response_id = r.response_id
WHERE r.response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY r.response_id, a.agency_id;
