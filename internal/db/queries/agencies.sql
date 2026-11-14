-- name: ListServerAgencies :many
-- The agencies recorded for one OBA server, oldest ID first. With
-- external_agency_id set, only that server's agency with that OBA agency ID
-- (zero or one row: the pair is unique).
SELECT agency_id, server_id, external_agency_id, current_name
FROM agency
WHERE server_id = sqlc.arg('server_id')
  AND (sqlc.narg('external_agency_id')::text IS NULL
       OR external_agency_id = sqlc.narg('external_agency_id'))
ORDER BY agency_id;

-- name: GetAgency :one
-- One agency by ID.
SELECT agency_id, server_id, external_agency_id, current_name
FROM agency
WHERE agency_id = $1;

-- name: ListAgenciesByIDs :many
-- The agencies with the given IDs, for the include loader. IDs with no row
-- are left out.
SELECT agency_id, server_id, external_agency_id, current_name
FROM agency
WHERE agency_id = ANY(sqlc.arg('agency_ids')::bigint[])
ORDER BY agency_id;
