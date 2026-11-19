-- name: GetVehicleAnomalyEpisode :one
SELECT sqlc.embed(v), opening_link.vehicle_position_evidence_id AS opening_evidence_id
FROM vehicle_anomaly_episode_view v
LEFT JOIN vehicle_anomaly_episode_evidence opening_link
    ON opening_link.vehicle_anomaly_episode_id = v.vehicle_anomaly_episode_id
   AND opening_link.evidence_role = 'opening'
WHERE v.vehicle_anomaly_episode_id = $1;
