-- name: GetOBAMetricsResponse :one
-- One OBA metrics response by ID, for E20.
SELECT response_id, server_id, observed_at, upstream_current_time, api_version
FROM onebusaway_metrics_response
WHERE response_id = sqlc.arg('response_id');

-- name: ListOBAMetricsResponsesWithAgencies :many
-- Headers and agency counts for every ID in one batched read, for the E20 include loader.
-- A response with no agency rows still returns one row, with NULL agency columns.
SELECT r.response_id, r.server_id, r.observed_at, r.upstream_current_time, r.api_version,
       a.agency_id, a.response_complete, a.unmatched_stop_count, a.unmatched_trip_count,
       a.matched_stop_count, a.matched_trip_count
FROM onebusaway_metrics_response AS r
LEFT JOIN onebusaway_metrics_response_agency AS a ON a.response_id = r.response_id
WHERE r.response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY r.response_id, a.agency_id;

-- name: ListOBAMetricsResponseAgencies :many
-- Each agency's counts for the given responses, agency_id ascending within each response.
SELECT response_id, agency_id, response_complete, unmatched_stop_count, unmatched_trip_count,
       matched_stop_count, matched_trip_count
FROM onebusaway_metrics_response_agency
WHERE response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY response_id, agency_id;
