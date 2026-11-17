-- name: ListReportedTrips :many
SELECT reported_trip_id, agency_id, oba_trip_id
FROM reported_trip
WHERE agency_id = sqlc.arg('agency_id') AND oba_trip_id = sqlc.arg('oba_trip_id');
