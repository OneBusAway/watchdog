# Metrics Documentation with Interpretation Guides

This document describes all Prometheus metrics exposed by the application, their purpose, labels, and units, and provides **interpretation guidance** for operators.

Metrics follow [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/) and are grouped by subsystem.

**Label convention:** Every metric that carries an `agency_id` label also carries a `agency_name` label (the configured `name` of the watched OBA server). This is a 1:1 mapping — config validation rejects duplicate `agency_id`s — so operators can identify an agency by its human-readable name even when the ID is not descriptive, e.g. in Grafana legends `{{agency_name}} ({{agency_id}})` or a PromQL filter on `agency_name`. Because the pairing is fixed per `agency_id`, the `agency_name` label adds no extra cardinality to any series.

---

## 1. API Availability

| Metric Name      | Type  | Labels                    | Unit          | Description                                                        |
| ---------------- | ----- | ------------------------- | ------------- | ------------------------------------------------------------------ |
| `oba_api_status` | Gauge | `agency_id`, `agency_name`, `server_url` | boolean (0/1) | Status of the OneBusAway API Server (0 = not working, 1 = working). One series per probed endpoint: `server_url` carries the full sanitized endpoint path, so `/api/where/current-time.json` and `/api/where/metrics.json` are reported separately. |

**Interpretation Guide:**  
- **Normal:** Always `1` (working).  
- **Investigate if:** Any server drops to `0` for more than 1–2 scrape intervals.  
- **Possible causes:** Server downtime, network issues, wrong URL.  
- **Example alert:**  
```promql
  # one series per endpoint; aggregate to alert once per agency
  min by (agency_id) (oba_api_status) == 0
```

---
## 2. GTFS Bundle Expiration

| Metric Name                                  | Type  | Labels      | Unit | Description                                     |
| -------------------------------------------- | ----- | ----------- | ---- | ----------------------------------------------- |
| `gtfs_bundle_days_until_earliest_expiration` | Gauge | `agency_id`, `agency_name` | days | Days until the earliest GTFS bundle expiration. |
| `gtfs_bundle_days_until_latest_expiration`   | Gauge | `agency_id`, `agency_name` | days | Days until the latest GTFS bundle expiration.   |
| `gtfs_bundle_last_fetched_timestamp_seconds` | Gauge | `agency_id`, `agency_name` | unix_timestamp | When Watchdog last downloaded the server's GTFS static bundle. |

**Interpretation Guide:**

- **Normal:** No official GTFS-mandated threshold , operators should set according to agency update policy.
- **Investigate if:** Days until expiration falls below internal SLA (e.g., < 3 days).
- **Possible causes:** Expired or unupdated GTFS feed.
- **Spec reference:** GTFS [calendar.txt](https://gtfs.org/documentation/schedule/reference/#calendartxt) and GTFS [calendar_dates.txt](https://gtfs.org/documentation/schedule/reference/#calendar_datestxt) define service date ranges but do **not** mandate minimum lead time.
- **Example alert:**
```promql
    gtfs_bundle_days_until_earliest_expiration < 3
```
---
## 3. Tracked Agencies

| Metric Name                    | Type  | Labels                              | Unit    | Description                                                              |
| ------------------------------ | ----- | ----------------------------------- | ------- | ------------------------------------------------------------------------ |
| `oba_tracked_agencies_count`   | Gauge | (none)                              | count   | Number of agencies currently tracked by Watchdog (validated config entries). |
| `oba_tracked_agencies_info`    | Gauge | `agency_id`, `agency_name`, `server_url` | presence | One series per tracked agency (always 1), listing the agencies themselves. |

**Interpretation Guide:**
- **Normal:** `oba_tracked_agencies_count` equals the number of servers in the config that passed validation.
- **Investigate if:** The count doesn't match the number of servers you expect in the config.
- **Possible causes:** A config entry failed validation (missing required fields, duplicate `agency_id`) and was dropped; or a refresh source (see `--config-url`) removed an agency.
- **Notes:**
  - These metrics are emitted once at startup and re-emitted only when the tracked set changes (a remote config refresh adds or removes an agency) — never on the periodic collection tick.
  - Stale series are pruned when an agency is removed, so `sum(oba_tracked_agencies_info) == oba_tracked_agencies_count`.
- **Example alert:**
```promql
  oba_tracked_agencies_count < 1
```

---
## 4. Vehicle & GTFS-RT Data Quality

| Metric Name                                | Type    | Labels                                 | Unit          | Description                                                   |
| ------------------------------------------ | ------- | -------------------------------------- | ------------- | ------------------------------------------------------------- |
| `realtime_vehicle_positions_count_gtfs_rt` | Gauge   | `agency_id`, `agency_name`           | count         | Number of realtime vehicle positions in the GTFS-RT feed.     |
| `oba_agency_active_vehicles_count`         | Gauge   | `agency_id`, `agency_name`           | count         | Number of active vehicles reported for the agency by the OBA vehicles-for-agency API. |
| `vehicle_position_report_interval_seconds` | Gauge   | `vehicle_id`, `agency_id`, `agency_name` | seconds       | Time since each vehicle last reported a GTFS-RT position.     |
| `vehicle_report_total`                     | Counter | `vehicle_id`, `agency_id`, `agency_name` | count         | Total number of GTFS-RT updates received per vehicle.         |
| `gtfs_rt_vehicle_computed_speed`           | Gauge   | `vehicle_id`, `agency_id`, `agency_name` | m/s           | Computed vehicle speed from GTFS-RT positions.                |
| `gtfs_rt_vehicle_speed_discrepancy_ratio`  | Gauge   | `vehicle_id`, `agency_id`, `agency_name` | ratio         | Ratio of computed to reported vehicle speed.                  |
| `gtfs_rt_invalid_vehicle_coordinates`      | Gauge   | `agency_id`, `agency_name`           | count         | Number of GTFS-RT vehicle positions with invalid coordinates. |
| `gtfs_rt_stopped_out_of_bounds_vehicles`   | Gauge   | `agency_id`, `agency_name`           | count         | Vehicles outside bounding box while stopped.                  |
| `gtfs_rt_tracked_vehicles_count`           | Gauge   | `agency_id`, `agency_name`           | count         | Number of vehicles currently being tracked.                   |

**Interpretation Guide:**
- **Vehicle counts:** Sudden drop may indicate feed outage.
- **Report intervals:** If significantly longer than agency update policy, data is stale.
- **Speed discrepancy ratio:** Persistent high ratios may mean faulty onboard GPS.
- **Invalid coordinates:** If >0, indicates bad GPS or malformed feed data.
- **Spec reference:**
    - [GTFS-RT VehiclePositions](https://gtfs.org/documentation/realtime/reference/#message-vehicleposition) requires timely updates but does not mandate exact intervals.
    - Position data must use [WGS-84 coordinates](https://gtfs.org/documentation/realtime/reference/#message-position).
---
## 5. OBA REST API Metrics

| Metric Name                          | Type  | Labels                                                   | Unit    | Description                                        |
| ------------------------------------ | ----- | -------------------------------------------------------- | ------- | -------------------------------------------------- |
| `oba_realtime_records_count`         | Gauge | `agency_id`, `agency_name`                               | count   | Total realtime records received.                   |
| `oba_realtime_trips_matched_count`   | Gauge | `agency_id`, `agency_name`                               | count   | Number of matched realtime trips.                  |
| `oba_realtime_trips_unmatched_count` | Gauge | `agency_id`, `agency_name`                               | count   | Number of unmatched realtime trips.                |
| `oba_scheduled_trips_count`          | Gauge | `agency_id`, `agency_name`                               | count   | Number of scheduled trips.                         |
| `oba_stops_matched_count`            | Gauge | `agency_id`, `agency_name`                               | count   | Number of matched stops.                           |
| `oba_stops_unmatched_count`          | Gauge | `agency_id`, `agency_name`                               | count   | Number of unmatched stops.                         |
| `oba_realtime_trip_match_ratio`      | Gauge | `agency_id`, `agency_name`                               | ratio   | Ratio of matched realtime trips to total trips.    |
| `oba_stop_match_ratio`               | Gauge | `agency_id`, `agency_name`                               | ratio   | Ratio of matched stops to total stops.             |
| `oba_time_since_last_update_seconds` | Gauge | `agency_id`, `agency_name`                               | seconds | Time since last realtime update.                   |
| `oba_unmatched_stop_info`            | Gauge | `agency_id`, `agency_name`, `stop_id`, `stop_name`, `lat`, `lon` | N/A     | Presence marker (always 1) for unmatched stops from static GTFS, with location as labels. |
| `oba_unmatched_stop_unresolved`      | Gauge | `agency_id`, `agency_name`                               | count   | Number of stop IDs OBA reported as unmatched that Watchdog could not resolve against its local GTFS bundle. |
| `oba_unmatched_stop_cluster_count`   | Gauge | `agency_id`, `agency_name`, `station_id`, `cluster_id`, `cluster_lat`, `cluster_lon` | count   | Number of unmatched stops grouped by station and S2 spatial cluster.      |

**Interpretation Guide:**
- **Unmatched stop clusters:** Identify systemic coverage gaps. Each series is one `(station_id, cluster_id)` pair:
    - `cluster_id` is the S2 cell (level 13, ~850–1225 m spatial resolution) derived from the unmatched stops' **own** coordinates — never fetched per station, so no N+1 API calls.
    - `cluster_lat` / `cluster_lon` are the center of that S2 cell, so clusters can be plotted on a map or joined by coordinates without decoding the ID.
    - `station_id` is the root parent station ID when the stops belong to a station hierarchy, or `no_station` when they do not. A large station can span several S2 cells, yielding one series per `(station_id, cluster_id)` pair; group by `cluster_id` for spatial aggregation and by `station_id` for per-station totals.
- **Time since update:** If unusually high, real-time feed is stale.
- **`oba_unmatched_stop_info` retention:** Each series is emitted at every scrape and pruned 24 hours after the stop last appeared unmatched. A `1` means the stop was unmatched at some point in the last 24 hours; history is preserved by Prometheus itself — use range queries to separate days:
```promql
  # unmatched at some point during the last day
  max_over_time(oba_unmatched_stop_info{agency_id="unitrans"}[1d])
  # flapping signal (value changes during the day)
  changes(oba_unmatched_stop_info{agency_id="unitrans"}[1d])
  # daily recording rule to bucket unmatched stops per calendar day
  sum by (agency_id, stop_id) (max_over_time(oba_unmatched_stop_info[1d]))
```
- **`oba_unmatched_stop_cluster_count` retention:** Cluster series follow the same 24h TTL as `oba_unmatched_stop_info` — a cluster's last reported count is retained until the cluster has not appeared for 24 hours, then the series is pruned. Use range queries to reconstruct historical cluster membership.
- **`oba_unmatched_stop_unresolved`:** `> 0` signals the OBA server is matching against a static bundle that differs from the one Watchdog downloaded (e.g., bundle refresh timing), so lookups silently dropped. Correlate with `gtfs_bundle_last_fetched_timestamp_seconds` to see how stale Watchdog's snapshot is.
- **Example alert:**
```promql
  sum by (agency_id) (oba_unmatched_stop_unresolved) > 0
```
---
## 6. Outgoing HTTP Requests

| Metric Name                              | Type      | Labels                         | Unit    | Description                                          |
| ---------------------------------------- | --------- | ------------------------------ | ------- | ---------------------------------------------------- |
| `http_outgoing_request_duration_seconds` | Histogram | `url`, `method`, `status_code` | seconds | Duration of outgoing HTTP requests to external APIs. |

**Interpretation Guide:**
- **Normal:** Most requests should be within a small range.    
- **Investigate if:** Slow spikes or sustained latency above internal performance thresholds.