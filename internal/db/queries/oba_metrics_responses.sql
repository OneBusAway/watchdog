-- name: GetOBAMetricsResponse :one
-- One OBA metrics response by ID, for E20.
SELECT response_id, server_id, observed_at, upstream_current_time, api_version
FROM onebusaway_metrics_response
WHERE response_id = sqlc.arg('response_id');

-- name: ListOBAMetricsResponses :many
-- The response headers for every ID, for the E20 include loader's single batched read.
SELECT response_id, server_id, observed_at, upstream_current_time, api_version
FROM onebusaway_metrics_response
WHERE response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY response_id;

-- name: ListOBAMetricsResponseAgencies :many
-- Each agency's counts for the given responses, agency_id ascending within each response.
SELECT response_id, agency_id, response_complete, unmatched_stop_count, unmatched_trip_count,
       matched_stop_count, matched_trip_count
FROM onebusaway_metrics_response_agency
WHERE response_id = ANY(sqlc.arg('response_ids')::bigint[])
ORDER BY response_id, agency_id;
