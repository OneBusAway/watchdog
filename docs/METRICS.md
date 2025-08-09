# Metrics Documentation with Interpretation Guides

This document describes all Prometheus metrics exposed by the application, their purpose, labels, and units, and provides **interpretation guidance** for operators.

Metrics follow [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/) and are grouped by subsystem.

---

## 1. API Availability

| Metric Name      | Type  | Labels                    | Unit          | Description                                                        |
| ---------------- | ----- | ------------------------- | ------------- | ------------------------------------------------------------------ |
| `oba_api_status` | Gauge | `server_id`, `server_url` | boolean (0/1) | Status of the OneBusAway API Server (0 = not working, 1 = working) |

**Interpretation Guide:**  
- **Normal:** Always `1` (working).  
- **Investigate if:** Any server drops to `0` for more than 1–2 scrape intervals.  
- **Possible causes:** Server downtime, network issues, wrong URL.  
- **Example alert:**  
```promql
  oba_api_status == 0
```

---
## 2. GTFS Bundle Expiration

| Metric Name                                  | Type  | Labels      | Unit | Description                                     |
| -------------------------------------------- | ----- | ----------- | ---- | ----------------------------------------------- |
| `gtfs_bundle_days_until_earliest_expiration` | Gauge | `server_id` | days | Days until the earliest GTFS bundle expiration. |
| `gtfs_bundle_days_until_latest_expiration`   | Gauge | `server_id` | days | Days until the latest GTFS bundle expiration.   |

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
## 3. Agency Data Consistency

| Metric Name                         | Type  | Labels      | Unit          | Description                                                                 |
| ----------------------------------- | ----- | ----------- | ------------- | --------------------------------------------------------------------------- |
| `oba_agencies_in_static_gtfs`       | Gauge | `server_id` | count         | Number of agencies in the static GTFS file.                                 |
| `oba_agencies_in_coverage_endpoint` | Gauge | `server_id` | count         | Number of agencies in the agencies-with-coverage endpoint.                  |
| `oba_agencies_match`                | Gauge | `server_id` | boolean (0/1) | Whether the agency count matches between static GTFS and coverage endpoint. |

**Interpretation Guide:**
- **Normal:** `oba_agencies_match` = `1`.
- **Investigate if:** `oba_agencies_match` = `0` or large difference between counts.
- **Possible causes:** Partial GTFS updates, API coverage issues, missing agencies.
- **Spec reference:** GTFS [agency.txt](https://gtfs.org/documentation/schedule/reference/#agencytxt) requires at least one agency but does not define count-matching rules.

---
## 4. Vehicle & GTFS-RT Data Quality

| Metric Name                                | Type    | Labels                                 | Unit          | Description                                                   |
| ------------------------------------------ | ------- | -------------------------------------- | ------------- | ------------------------------------------------------------- |
| `realtime_vehicle_positions_count_gtfs_rt` | Gauge   | `gtfs_rt_url`, `server_id`             | count         | Number of realtime vehicle positions in the GTFS-RT feed.     |
| `vehicle_count_api`                        | Gauge   | `agency_id`, `server_id`               | count         | Number of vehicles in the API response.                       |
| `vehicle_count_match`                      | Gauge   | `agency_id`, `server_id`               | boolean (0/1) | Whether vehicle count matches between API and GTFS-RT.        |
| `vehicle_position_report_interval_seconds` | Gauge   | `vehicle_id`, `server_id`              | seconds       | Time since each vehicle last reported a GTFS-RT position.     |
| `vehicle_report_total`                     | Counter | `vehicle_id`, `server_id`              | count         | Total number of GTFS-RT updates received per vehicle.         |
| `gtfs_rt_vehicle_computed_speed`           | Gauge   | `vehicle_id`, `agency_id`, `server_id` | m/s           | Computed vehicle speed from GTFS-RT positions.                |
| `gtfs_rt_vehicle_speed_discrepancy_ratio`  | Gauge   | `vehicle_id`, `agency_id`, `server_id` | ratio         | Ratio of computed to reported vehicle speed.                  |
| `gtfs_rt_invalid_vehicle_coordinates`      | Gauge   | `server_id`                            | count         | Number of GTFS-RT vehicle positions with invalid coordinates. |
| `gtfs_rt_stopped_out_of_bounds_vehicles`   | Gauge   | `server_id`                            | count         | Vehicles outside bounding box while stopped.                  |
| `gtfs_rt_tracked_vehicles_count`           | Gauge   | `server_id`                            | count         | Number of vehicles currently being tracked.                   |

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
| `oba_agencies_with_coverage_count`   | Gauge | `server`                                                 | count   | Number of agencies with coverage.                  |
| `oba_realtime_records_total`         | Gauge | `server`, `agency`                                       | count   | Total realtime records received.                   |
| `oba_realtime_trips_matched_count`   | Gauge | `server`, `agency`                                       | count   | Number of matched realtime trips.                  |
| `oba_realtime_trips_unmatched_count` | Gauge | `server`, `agency`                                       | count   | Number of unmatched realtime trips.                |
| `oba_scheduled_trips_count`          | Gauge | `server`, `agency`                                       | count   | Number of scheduled trips.                         |
| `oba_stops_matched_count`            | Gauge | `server`, `agency`                                       | count   | Number of matched stops.                           |
| `oba_stops_unmatched_count`          | Gauge | `server`, `agency`                                       | count   | Number of unmatched stops.                         |
| `oba_realtime_trip_match_ratio`      | Gauge | `server`, `agency`                                       | ratio   | Ratio of matched realtime trips to total trips.    |
| `oba_stop_match_ratio`               | Gauge | `server`, `agency`                                       | ratio   | Ratio of matched stops to total stops.             |
| `oba_time_since_last_update_seconds` | Gauge | `server`, `agency`                                       | seconds | Time since last realtime update.                   |
| `oba_unmatched_stop_location`        | Gauge | `server`, `agency`, `stop_id`, `stop_name`, `lat`, `lon` | N/A     | Location info of unmatched stops from static GTFS. |
| `oba_unmatched_stop_cluster_count`   | Gauge | `server`, `agency`, `cluster_id`, `cluster_type`         | count   | Number of unmatched stops grouped by cluster.      |

**Interpretation Guide:**
- **Unmatched stop clusters:** Identify systemic coverage gaps.
- **Time since update:** If unusually high, real-time feed is stale.
---
## 6. Outgoing HTTP Requests

| Metric Name                              | Type      | Labels                         | Unit    | Description                                          |
| ---------------------------------------- | --------- | ------------------------------ | ------- | ---------------------------------------------------- |
| `http_outgoing_request_duration_seconds` | Histogram | `url`, `method`, `status_code` | seconds | Duration of outgoing HTTP requests to external APIs. |

**Interpretation Guide:**
- **Normal:** Most requests should be within a small range.    
- **Investigate if:** Slow spikes or sustained latency above internal performance thresholds.