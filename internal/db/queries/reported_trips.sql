-- name: ListReportedTrips :many
SELECT reported_trip_id, agency_id, oba_trip_id
FROM reported_trip
WHERE agency_id = sqlc.arg('agency_id') AND oba_trip_id = sqlc.arg('oba_trip_id');

-- name: GetReportedTrip :one
SELECT reported_trip_id, agency_id, oba_trip_id
FROM reported_trip
WHERE reported_trip_id = sqlc.arg('reported_trip_id');

-- name: ListReportedTripsByIDs :many
-- The reported trips with the given IDs, for the include loader. IDs with no
-- row are left out.
SELECT reported_trip_id, agency_id, oba_trip_id
FROM reported_trip
WHERE reported_trip_id = ANY(sqlc.arg('reported_trip_ids')::bigint[])
ORDER BY reported_trip_id;
