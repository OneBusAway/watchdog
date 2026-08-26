# Metrics Documentation with Interpretation Guides

This document describes all Prometheus metrics exposed by the application, their purpose, labels, and units, and provides **interpretation guidance** for operators.

Metrics follow [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/) and are grouped by subsystem.

**Label convention:** Every metric that carries an `agency_id` label also carries an `agency_name` label (the configured `name` of the watched OBA server) and a `server_url` label (the sanitized `oba_base_url` of the deployment). In a single deployment `agency_id`/`agency_name` is effectively a 1:1 mapping, so operators can identify an agency by its human-readable name even when the ID is not descriptive, e.g. in Grafana legends `{{agency_name}} ({{agency_id}})`, and `server_url` disambiguates deployments that legitimately reuse the same `agency_id`. Because the pairing is fixed per deployment, these labels add no extra cardinality to any series.

**Identity — the Server Key:** The unique identity of a monitored deployment is the composite of its `oba_base_url` plus `agency_id`. GTFS `agency_id` values are only unique *within* a single OBA server, so two distinct deployments can legitimately reuse the same `agency_id` (e.g. both use `"1"` or `"MTA"`). All Watchdog stores (GTFS static/real-time bundles, bounding boxes, backoff state, vehicle last-seen, unmatched-stop tracking) and config validation are keyed on this composite, so both deployments are monitored independently. Config validation only rejects *exact* duplicates — the same `oba_base_url` **and** `agency_id` — since those are genuine mistakes.

**Shared `agency_id` across deployments:** The metric series labeled with `agency_id`/`agency_name` also carry `server_url` (the sanitized base URL), so the `(agency_id, server_url)` pair is a unique deployment identity mirroring the composite `ServerKey`. Observations from two deployments that share an `agency_id` no longer collide — each keeps its own series. The only exception is the scalar `oba_tracked_agencies_count`, which has no per-deployment series.

---

## 1. API Availability

| Metric Name      | Type  | Labels                    | Unit          | Description                                                        |
| ---------------- | ----- | ------------------------- | ------------- | ------------------------------------------------------------------ |
| `oba_api_status` | Gauge | `server_name`, `server_url` | boolean (0/1) | Status of the OneBusAway API Server (0 = not working, 1 = working). One series per server: the ping targets `/api/where/current-time.json`, which takes no agency parameter, so this metric carries no `agency_id` and `server_url` is the bare sanitized base URL — the same value every per-agency metric uses, so the two join. |

**Interpretation Guide:**  
- **Normal:** Always `1` (working).  
- **Investigate if:** Any server drops to `0` for more than 1–2 scrape intervals.  
- **Possible causes:** Server downtime, network issues, wrong URL.  
- **Example alert:**  
```promql
  # one series per server; oba_api_status carries no agency_id
  oba_api_status == 0
```

---
## 2. GTFS Bundle Expiration

| Metric Name                                  | Type  | Labels      | Unit | Description                                     |
| -------------------------------------------- | ----- | ----------- | ---- | ----------------------------------------------- |
| `gtfs_bundle_days_until_earliest_expiration` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | days | Days until the earliest GTFS bundle expiration. |
| `gtfs_bundle_days_until_latest_expiration`   | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | days | Days until the latest GTFS bundle expiration.   |
| `gtfs_bundle_last_fetched_timestamp_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | When Watchdog last downloaded the server's GTFS static bundle. |

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
| `oba_tracked_agencies_info`    | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | presence | One series per tracked agency (always 1), listing the agencies themselves. |

**Interpretation Guide:**
- **Normal:** `oba_tracked_agencies_count` equals the number of servers in the config that passed validation.
- **Investigate if:** The count doesn't match the number of servers you expect in the config.
- **Possible causes:** A config entry failed validation (missing required fields, exact `oba_base_url` + `agency_id` duplicate) and was dropped; or a refresh source (see `--config-url`) removed an agency.
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
| `realtime_vehicle_positions_count_gtfs_rt` | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of realtime vehicle positions in the GTFS-RT feeds. Each feed is an independent vehicle namespace, so the same vehicle ID in two feeds counts twice (two distinct physical vehicles). |
| `oba_agency_active_vehicles_count`         | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of active vehicles reported for the agency by the OBA vehicles-for-agency API. |
| `vehicle_position_report_interval_seconds` | Gauge   | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | seconds       | Time since each vehicle last reported a GTFS-RT position.     |
| `vehicle_report_total`                     | Counter | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | count         | Total number of GTFS-RT updates received per vehicle.         |
| `gtfs_rt_vehicle_computed_speed`           | Gauge   | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | m/s           | Computed vehicle speed from GTFS-RT positions.                |
| `gtfs_rt_vehicle_speed_discrepancy_ratio`  | Gauge   | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | ratio         | Ratio of computed to reported vehicle speed.                  |
| `gtfs_rt_invalid_vehicle_coordinates`      | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of GTFS-RT vehicle positions with invalid coordinates. In server-mode, vehicles that cannot be attributed to an agency are counted under an empty `agency_id`, so the series always sum to the server-wide count. |
| `gtfs_rt_stopped_out_of_bounds_vehicles`   | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Vehicles outside bounding box while stopped. Same empty-`agency_id` fallback as the metric above. |
| `gtfs_rt_tracked_vehicles_count`           | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of vehicles currently being tracked.                   |
| `gtfs_rt_unattributed_vehicles_count`      | Gauge   | `server_name`, `server_url`                        | count         | Server-mode only. Vehicles the per-vehicle series could not cover: route unresolvable to a reported agency, or no vehicle ID at all. |

**Interpretation Guide:**
- **Vehicle counts:** Sudden drop may indicate feed outage.
- **Per-vehicle metrics and the `feed` label:** GTFS-RT vehicle IDs are only unique *within a single feed*. A deployment may scrape multiple feeds whose vehicles all belong to the same umbrella agency, and two feeds can legitimately reuse the same numeric vehicle ID (e.g. `"101"`) for different physical vehicles. Watchdog therefore treats each feed as an independent vehicle namespace: cross-feed IDs are never deduplicated, and every vehicle is tagged with the zero-based index of the feed it came from (`feed = "0"`, `"1"`, … matching the order of `gtfs_rt_feeds` in the config). The four per-vehicle metrics (`vehicle_position_report_interval_seconds`, `vehicle_report_total`, `gtfs_rt_vehicle_computed_speed`, `gtfs_rt_vehicle_speed_discrepancy_ratio`) are labeled with `(vehicle_id, feed)`, so two different buses that happen to share a vehicle ID do not collide in one series. This also means `realtime_vehicle_positions_count_gtfs_rt` counts each feed's vehicles independently (no cross-feed dedup).
  - View one feed's vehicles:
  ```promql
  vehicle_report_total{feed="0"}
  ```
  - Aggregate across all feeds into one line per vehicle:
  ```promql
  sum by (vehicle_id) (vehicle_report_total{agency_id="unitrans"})
  # or, equivalently
  sum(vehicle_report_total{vehicle_id="101"}) without (feed)
  ```
  - Worst-case report interval per bus across all feeds (gauges roll up differently than counters):
  ```promql
  max by (vehicle_id) (vehicle_position_report_interval_seconds{agency_id="unitrans"})
  ```
- **Per-agency attribution in server-mode:** A server-scoped config entry (no `agency_id`) exposes one merged GTFS-RT feed covering several agencies. Watchdog walks that feed once per tick and attributes each vehicle to an agency by resolving its `TripDescriptor.route_id` against the agencies declared in the static feeds, so `agency_id` on every metric in this section means the agency that actually owns the vehicle. A vehicle is *unattributable* when its trip carries no `route_id`, its route is unknown to any static feed, or its route belongs to an agency the server is not currently reporting.
- **Where unattributable vehicles land:** No vehicle disappears entirely, but the metric that accounts for it differs, and the three paths below do not add up to a single tidy identity — do not write an alert that assumes they do:
  - **Per-vehicle series** (`vehicle_report_total`, `vehicle_position_report_interval_seconds`, `gtfs_rt_vehicle_computed_speed`, `gtfs_rt_vehicle_speed_discrepancy_ratio`) and `gtfs_rt_tracked_vehicles_count` and `realtime_vehicle_positions_count_gtfs_rt` omit them. They are counted instead in `gtfs_rt_unattributed_vehicles_count{server_name, server_url}`, along with vehicles carrying no vehicle ID (those have no `vehicle_id` label to be filed under). A persistently non-zero value there means the static feeds do not cover everything the RT feed references, or the feed is emitting malformed entities.
  - **Attributable vehicles with no usable position** are a third case, counted in neither of the above: they are omitted from the per-vehicle series and from `gtfs_rt_unattributed_vehicles_count`, and appear only in `gtfs_rt_invalid_vehicle_coordinates`. This is why `sum(realtime_vehicle_positions_count_gtfs_rt) + gtfs_rt_unattributed_vehicles_count` does not equal the feed size in either direction.
  - **The data-quality gauges** (`gtfs_rt_invalid_vehicle_coordinates`, `gtfs_rt_stopped_out_of_bounds_vehicles`) count them under the server-scoped series — the one with an empty `agency_id`/`agency_name`. Coordinate validity is judged *before* attribution precisely because the most malformed entities (no `TripDescriptor`, no position) are the ones attribution cannot place, and they are the ones these gauges exist to catch. So `sum by (server_url) (gtfs_rt_invalid_vehicle_coordinates)` is the true server-wide count, while the non-empty `agency_id` series give the breakdown:
  ```promql
  # server-wide, including unattributable vehicles
  sum by (server_url) (gtfs_rt_invalid_vehicle_coordinates)
  # just the vehicles that could not be placed with an agency
  gtfs_rt_invalid_vehicle_coordinates{agency_id=""}
  ```
  In agency-mode (an entry with an `agency_id`) every vehicle belongs to the configured agency by definition, so no empty-`agency_id` series is emitted and `gtfs_rt_unattributed_vehicles_count` is not published at all.
- **The bounding box is still server-wide:** `gtfs_rt_stopped_out_of_bounds_vehicles` is attributed per agency, but the box it tests against is computed over the union of *every* configured static feed's stops. On a multi-agency server a vehicle stopped in one agency's territory is validated against a rectangle covering all of them, so treat this metric as a loose bound rather than a precise one.
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
| `oba_realtime_records_count`         | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Total realtime records received.                   |
| `oba_realtime_trips_matched_count`   | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of matched realtime trips.                  |
| `oba_realtime_trips_unmatched_count` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of unmatched realtime trips.                |
| `oba_scheduled_trips_count`          | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of scheduled trips.                         |
| `oba_stops_matched_count`            | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of matched stops.                           |
| `oba_stops_unmatched_count`          | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of unmatched stops.                         |
| `oba_realtime_trip_match_ratio`      | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | ratio   | Ratio of matched realtime trips to total trips.    |
| `oba_stop_match_ratio`               | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | ratio   | Ratio of matched stops to total stops.             |
| `oba_time_since_last_update_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | seconds | Time since last realtime update.                   |
| `oba_unmatched_stop_info`            | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`, `stop_id`, `stop_name`, `lat`, `lon` | N/A     | Presence marker (always 1) for unmatched stops from static GTFS, with location as labels. |
| `oba_unmatched_stop_unresolved`      | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of stop IDs OBA reported as unmatched that Watchdog could not resolve against its local GTFS bundle. |
| `oba_unmatched_stop_cluster_count`   | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`, `station_id`, `cluster_id`, `cluster_lat`, `cluster_lon` | count   | Number of unmatched stops grouped by station and S2 spatial cluster.      |

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
