# Metrics Documentation with Interpretation Guides

This document describes all Prometheus metrics exposed by the application, their purpose, labels, and units, and provides **interpretation guidance** for operators.

Metrics follow [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/) and are grouped by subsystem.

**Label convention:** Every metric that carries an `agency_id` label also carries an `agency_name` label (the configured `name` of the watched OBA server) and a `server_url` label (the sanitized `oba_base_url` of the deployment). In a single deployment `agency_id`/`agency_name` is effectively a 1:1 mapping, so operators can identify an agency by its human-readable name even when the ID is not descriptive, e.g. in Grafana legends `{{agency_name}} ({{agency_id}})`, and `server_url` disambiguates deployments that legitimately reuse the same `agency_id`. Because the pairing is fixed per deployment, these labels add no extra cardinality to any series.

**Identity — the Server Key:** The unique identity of a monitored deployment is the composite of its `oba_base_url` plus `agency_id`. GTFS `agency_id` values are only unique *within* a single OBA server, so two distinct deployments can legitimately reuse the same `agency_id` (e.g. both use `"1"` or `"MTA"`). All Watchdog stores (GTFS static/real-time bundles, bounding boxes, backoff state, vehicle last-seen, unmatched-stop tracking) and config validation are keyed on this composite, so both deployments are monitored independently. Config validation only rejects *exact* duplicates — the same `oba_base_url` **and** `agency_id` — since those are genuine mistakes.

**Shared `agency_id` across deployments:** The metric series labeled with `agency_id`/`agency_name` also carry `server_url` (the sanitized base URL), so the `(agency_id, server_url)` pair is a unique deployment identity mirroring the composite `ServerKey`. Observations from two deployments that share an `agency_id` no longer collide — each keeps its own series. Server-scoped inventory metrics also carry `server_url`.

See [FRESHNESS.md](FRESHNESS.md) for the normative client contract. A recent Prometheus sample proves that `/metrics` was scraped, not that collection advanced.

## 0. Watchdog Collection Health

| Metric Name | Type | Labels | Unit | Description |
| --- | --- | --- | --- | --- |
| `watchdog_collection_interval_seconds` | Gauge | none | seconds | Configured realtime collection interval. |
| `watchdog_collection_last_completed_timestamp_seconds` | Gauge | none | unix_timestamp | When the sequential collection loop last completed a full cycle over the current server configuration. Not advanced while no servers are configured. |
| `watchdog_configured_servers` | Gauge | none | count | Number of valid servers currently configured. `0` means Watchdog is up but monitoring nothing (for example, a `--config-url` source that was empty or invalid at boot). |

Clients define the current-value window as `clamp(3 * interval, 60, 300)` seconds and require the completion timestamp to be within that window. `up == 1` without a recent completion means the metrics HTTP endpoint is reachable while collection is stalled.

## 0.1. Configured Server Identity

| Metric Name | Type | Labels | Unit | Description |
| --- | --- | --- | --- | --- |
| `watchdog_server_info` | Gauge | `server_url`, `service_slug`, `environment`, `organization` | presence | Configuration-source identity for a monitored server. The value is always `1`; labels are emitted only when all identity fields are configured. |

`service_slug` is a short, machine-readable identifier for the monitored service, supplied by the source of Watchdog's configuration. For example, `metro-transit-api` could identify a transit service. Watchdog treats the value as opaque: it does not parse it or depend on a particular hosting platform's naming rules. `environment` and `organization` are also supplied metadata. Existing configurations may omit all three fields; Watchdog continues monitoring those servers and simply omits their `watchdog_server_info` series.

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
| `gtfs_bundle_last_fetched_timestamp_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | When Watchdog last downloaded the server's GTFS static feed and stored its scoped snapshot. |
| `gtfs_static_feed_agency_mapping_info` | Gauge | `feed_url`, `agency_id`, `agency_name`, `server_name`, `server_url` | presence | Always 1. One series for each relationship proven from that source feed while it is parsed. It is not crossed with the server's OBA-reported agency set. |
| `gtfs_static_feed_agency_mapping_failure` | Gauge | `feed_url`, `server_name`, `server_url`, `reason` | presence | Always 1 when a successfully parsed source feed cannot be fully mapped. `reason` is bounded to `missing_agency`, `ambiguous_agency`, or `unknown_agency`. |
| `gtfs_static_bundle_current` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | boolean | `1` when the retained complete bundle is usable, the latest refresh attempt succeeded, and the last complete success is no more than 26 hours old. |
| `gtfs_static_bundle_usable` | Gauge | same agency labels | boolean | `1` when a retained complete bundle's latest service end date still covers the agency-local date. |
| `gtfs_static_refresh_last_attempt_timestamp_seconds` | Gauge | `server_name`, `server_url` | unix_timestamp | Time of the latest complete refresh attempt. |
| `gtfs_static_refresh_last_attempt_success` | Gauge | `server_name`, `server_url` | boolean | Result of the latest complete refresh attempt. |
| `gtfs_static_refresh_retrying` | Gauge | `server_name`, `server_url` | boolean | Whether the daily bounded retry campaign is active. |
| `gtfs_static_refresh_gave_up` | Gauge | `server_name`, `server_url` | boolean | Whether the current campaign exhausted its 12-hour budget. |
| `gtfs_static_feed_fetch_success` | Gauge | `feed_url`, `server_name`, `server_url` | boolean | Whether the latest campaign attempt for this configured static feed succeeded. |
| `gtfs_static_feed_last_attempt_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Time of the latest fetch attempt for this feed. |
| `gtfs_static_feed_last_successful_fetch_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Time of the latest successful fetch for this feed. Preserved across later failures. |
| `gtfs_static_feed_failure_since_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Start of the current failure streak, or `0` while healthy. |
| `gtfs_static_feed_conservative_mode` | Gauge | same feed labels | boolean | `1` when repeated or deterministic failures limit this feed to one recovery probe at the normal daily boundary. |
| `gtfs_static_feed_failure_info` | Gauge | feed labels plus `stage`, `reason` | presence | Bounded classification of the latest failure. Raw errors remain in logs and Sentry. |

**Interpretation Guide:**

- **Normal:** No official GTFS-mandated threshold , operators should set according to agency update policy.
- **Investigate if:** Days until expiration falls below internal SLA (e.g., < 3 days).
- **Possible causes:** Expired or unupdated GTFS feed.
- **Mapping info:** Server mode emits every distinct non-empty agency ID explicitly declared by each feed, including all agencies in a legitimate multi-agency feed. Agency mode emits only the configured agency when that ID is explicitly declared, or when a sole agency row omits `agency_id` and the configured scope supplies the identity. No series is inferred from `/api/where/metrics.json` coverage.
- **Mapping failures:** `missing_agency` means there is no agency row, or server mode has one agency row with no ID. `ambiguous_agency` means one or more blank-ID rows coexist with other agency rows, so not every relationship can be identified. `unknown_agency` is agency-mode only and means all rows have explicit IDs but none matches the configured agency. Proven explicit relationships may coexist with `ambiguous_agency` when other rows remain unidentified.
- **Refresh and cleanup:** A complete successful refresh replaces prior mapping/failure series for its parsed sources. A feed removed from configuration is retired on the next complete successful static refresh. A transient download/parse failure preserves the last successful classifications. Removing an entry/server retires all of its mapping series during config pruning.
- **Atomic refresh:** Every configured source must download and parse and the compact schedule must compile before Watchdog replaces the retained snapshot. A failure preserves the prior complete snapshot and marks it non-current.
- **Retry campaign and cache:** Startup, configuration changes, and daily refreshes use one managed campaign per configured entry. Successful compressed ZIP artifacts are held in a private temporary directory for that campaign, so retries fetch only failed feeds. A response is limited to 512 MiB and the campaign cache to 2 GiB. Publication still occurs only after the complete configured set parses, compiles, and validates. A changed or removed configuration cancels the old campaign and prevents its late publication.
- **Retry timing:** A failed campaign retries after 5m, 10m, 20m, 40m, 80m, then at most every 2h. There is no jitter. The campaign stops after 12h. HTTP 429 honors `Retry-After` when it is longer than the normal delay.
- **Conservative recovery:** Watchdog never edits configuration or permanently blacklists a feed. Invalid URLs and authentication failures enter conservative mode immediately; 404/410 do so after one failed daily campaign; network, 408/5xx, and unknown failures do so after seven failed daily campaigns; identical invalid archive, agency-discovery, or schedule content does so after three identical content hashes. Conservative feeds are probed once at the daily boundary. A successful probe clears the state and resumes a complete campaign immediately.
- **Current versus usable:** An unexpired retained snapshot can be usable while non-current. It becomes unusable on the agency-local date after its latest service end date. Gate current static-derived views on `gtfs_static_bundle_current`; show `usable == 1, current == 0` only as retained stale context.
- **Spec reference:** GTFS [calendar.txt](https://gtfs.org/documentation/schedule/reference/#calendartxt) and GTFS [calendar_dates.txt](https://gtfs.org/documentation/schedule/reference/#calendar_datestxt) define service date ranges but do **not** mandate minimum lead time.
- **Example alert:**
```promql
    gtfs_bundle_days_until_earliest_expiration < 3
```
---
## 3. Tracked Agencies

| Metric Name                    | Type  | Labels                              | Unit    | Description                                                              |
| ------------------------------ | ----- | ----------------------------------- | ------- | ------------------------------------------------------------------------ |
| `oba_tracked_agencies_count`   | Gauge | `server_name`, `server_url` | count   | Number of distinct agencies declared in the latest complete static snapshot for this server. In agency mode, 1 means the configured agency snapshot is present; 0 means it is not currently trackable from static data. |
| `oba_tracked_agencies_info`    | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | presence | One series per tracked agency (always 1), listing the agencies themselves. |

**Interpretation Guide:**
- **Normal:** In server mode, the count equals the number of distinct agency IDs declared by the server's configured static feeds. In agency mode, the count is 1 when the configured agency is present in the retained static snapshot and 0 otherwise.
- **Investigate if:** The count unexpectedly changes after a successful complete static refresh or is 0 when an agency snapshot is expected.
- **Possible causes:** A changed static feed, an agency removed or added in `agency.txt`, a config change, or no successfully published static snapshot yet.
- **Notes:**
  - Inventory reflects Watchdog's latest complete static snapshot, not the number of accepted configuration objects and not OBA's current realtime agency set.
  - `oba_tracked_agencies_info` has one series per distinct static agency ID, with the agency name, server name, and server URL.
  - These gauges refresh when configuration changes and after a complete static snapshot is published. Failed refreshes preserve the prior snapshot and inventory.
  - **TODO (#163):** compare this static agency set with the agency IDs reported by OBA's `metrics.json` API and expose alignment/mismatch status. Until then, `gtfs_static_agency_reported_by_oba` remains the separate OBA-side coverage signal.
- **Example alert:**
```promql
  oba_tracked_agencies_count < 1
```

---
## 4. Vehicle & GTFS-RT Data Quality

| Metric Name                                | Type    | Labels                                 | Unit          | Description                                                   |
| ------------------------------------------ | ------- | -------------------------------------- | ------------- | ------------------------------------------------------------- |
| `realtime_vehicle_positions_count_gtfs_rt` | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of realtime vehicle positions in the GTFS-RT feeds. Each feed is an independent vehicle namespace, so the same vehicle ID in two feeds counts twice (two distinct physical vehicles). |
| `gtfs_schedule_available` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | boolean | `1` only when every configured static feed in the latest refresh downloaded, parsed, and passed the strict compact-schedule compiler. `0` means schedule state is absent, stale after a failed refresh, incomplete, or invalid. |
| `gtfs_scheduled_service_active` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | boolean | `1` when at least one valid trip or frequency window is active at collection time. This value is meaningful only while `gtfs_schedule_available == 1`; unavailable schedules emit `0` rather than asserting either active or outside-service state. |
| `gtfs_rt_last_successful_fetch_timestamp_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | When Watchdog last successfully fetched and parsed all configured vehicle-position feeds for the server/agency scope. In server scope, `agency_id` and `agency_name` are empty. The metric is omitted when no GTFS-RT feeds are configured. This is Watchdog collection time, not the feeds' source-data timestamp. Use it to gate current-state feed metrics; Prometheus sample timestamps only prove Watchdog was scraped. |
| `gtfs_rt_feed_fetch_success` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`, `feed`, `feed_url` | boolean | Whether the latest fetch and parse of this configured feed succeeded. `feed` is its stable zero-based config index and `feed_url` is sanitized. |
| `gtfs_rt_feed_last_successful_fetch_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Watchdog time of the latest successful fetch and parse. |
| `gtfs_rt_feed_source_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Optional source time from `FeedHeader.timestamp`. Absent when the header has no timestamp and removed after a failed fetch. |
| `gtfs_rt_feed_payload_last_changed_timestamp_seconds` | Gauge | same feed labels | unix_timestamp | Watchdog time when the successful payload last changed. Comparison is canonical/order-independent and excludes the entire feed header. |
| `gtfs_rt_feed_vehicle_entities` | Gauge | same feed labels | count | Vehicle entities in the latest successful payload. Removed after a failed fetch rather than emitted as zero. |
| `gtfs_rt_feed_vehicle_timestamp_missing_count` | Gauge | same feed labels | count | Vehicle entities without `VehiclePosition.timestamp` in the latest successful payload. Removed after a failed fetch rather than emitted as zero. |
| `oba_agency_active_vehicles_count`         | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of active vehicles reported for the agency by the OBA vehicles-for-agency API. |
| `oba_vehicles_last_successful_fetch_timestamp_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | When Watchdog last received a successful vehicles-for-agency response. This is Watchdog collection time, not the returned vehicles' source-data timestamp. |
| `gtfs_rt_vehicle_source_timestamp_seconds` | Gauge | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | Optional source-provided vehicle timestamp. No series is emitted when it is missing; Watchdog time is never substituted. |
| `gtfs_rt_vehicle_state_last_changed_timestamp_seconds` | Gauge | same vehicle labels | unix_timestamp | Watchdog time when semantic vehicle content last changed. The vehicle source timestamp is excluded from state. |
| `gtfs_rt_vehicle_source_timestamp_advances_total` | Counter | `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | count | Aggregate count of vehicle source-timestamp advances. It deliberately has no `vehicle_id` label. |
| `gtfs_rt_vehicle_state_changes_total` | Counter | `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | count | Aggregate count of semantic vehicle-state transitions, excluding timestamp-only changes. It deliberately has no `vehicle_id` label. |
| `gtfs_rt_vehicle_computed_speed`           | Gauge   | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | m/s           | Computed vehicle speed from GTFS-RT positions.                |
| `gtfs_rt_vehicle_speed_discrepancy_ratio`  | Gauge   | `vehicle_id`, `feed`, `agency_id`, `agency_name`, `server_name`, `server_url` | ratio         | Ratio of computed to reported vehicle speed.                  |
| `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds` | Gauge | same vehicle labels | unix_timestamp | Watchdog observation time when speed was last recomputed from two valid advancing source timestamps. |
| `gtfs_rt_invalid_vehicle_coordinates`      | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of GTFS-RT vehicle positions with invalid coordinates. In server-mode, vehicles that cannot be attributed to an agency are counted under an empty `agency_id`, so the series always sum to the server-wide count. |
| `gtfs_rt_stopped_out_of_bounds_vehicles`   | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Vehicles outside the applicable static bounding box while stopped. In server-mode, unattributed vehicles use the empty-`agency_id` server-scoped box. |
| `gtfs_rt_tracked_vehicles_count`           | Gauge   | `agency_id`, `agency_name`, `server_name`, `server_url`           | count         | Number of attributable vehicle IDs currently held in the freshness state store. |
| `gtfs_rt_unattributed_vehicles_count`      | Gauge   | `server_name`, `server_url`                        | count         | Server-mode only. Vehicles the per-vehicle series could not cover because neither route/trip attribution resolved to an agency reported by OBA, or because the vehicle has no ID. |
| `gtfs_rt_unattributed_vehicles_by_reason_count` | Gauge | `server_name`, `server_url`, `reason` | count | Server-mode only. Exact partition of `gtfs_rt_unattributed_vehicles_count` by one of the six bounded reasons below. Every reason is emitted on every successful tick, including zero. |
| `gtfs_rt_unattributed_vehicle_potential_agency_associations_count` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | count | Server-mode only. Potential agency associations for unattributed vehicles. One vehicle may increment multiple agencies, so this metric is diagnostic and must not be summed as a distinct vehicle count. |

**Interpretation Guide:**
- **Schedule-aware zero counts:** Interpret zero vehicles as expected outside service only when `gtfs_static_bundle_usable == 1`, `gtfs_schedule_available == 1`, and `gtfs_scheduled_service_active == 0`. `available == 0` is unknown, never confidently outside service. Treat zero as potentially unexpected only when both schedule gauges equal `1`, static data is usable, every configured RT feed currently reports success, every feed's last-success timestamp is within the dynamic freshness window for the same agency scope, and the collection loop completed recently. No zero-vehicle alert is installed by default because thresholds and expected deadhead/reporting behavior are agency-specific.
- **Safe unexpected-zero query:**
  ```promql
  (
    ((realtime_vehicle_positions_count_gtfs_rt == 0)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_schedule_available == 1)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_scheduled_service_active == 1)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_static_bundle_usable == 1)
      and on (agency_id, server_name, server_url)
        (min by (agency_id, server_name, server_url) (gtfs_rt_feed_fetch_success{agency_id!=""}) == 1)
      and on (agency_id, server_name, server_url)
        ((time() - min by (agency_id, server_name, server_url)
            (gtfs_rt_feed_last_successful_fetch_timestamp_seconds{agency_id!=""}))
          < on() group_left clamp_max(clamp_min(watchdog_collection_interval_seconds * 3, 60), 300)))
    or
    ((realtime_vehicle_positions_count_gtfs_rt == 0)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_schedule_available == 1)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_scheduled_service_active == 1)
      and on (agency_id, agency_name, server_name, server_url) (gtfs_static_bundle_usable == 1)
      and on (server_name, server_url)
        (min by (server_name, server_url) (gtfs_rt_feed_fetch_success{agency_id=""}) == 1)
      and on (server_name, server_url)
        ((time() - min by (server_name, server_url)
            (gtfs_rt_feed_last_successful_fetch_timestamp_seconds{agency_id=""}))
          < on() group_left clamp_max(clamp_min(watchdog_collection_interval_seconds * 3, 60), 300)))
  )
  and on()
    ((time() - watchdog_collection_last_completed_timestamp_seconds)
      < on() group_left clamp_max(clamp_min(watchdog_collection_interval_seconds * 3, 60), 300))
  ```
- **Compilation and freshness semantics:** Compilation happens once while each successfully parsed static feed still has trips, stop times, and frequencies in memory. The retained immutable index contains only agency ownership, timezone, calendar rules and exceptions, compact service windows, sanitized source-feed provenance, maximum overnight offset, and completeness state. It is evaluated on each collection tick without reparsing. All configured feeds must compile before the per-agency snapshots are replaced atomically. A transient download/parse/validation failure preserves the last good snapshot internally but sets schedule availability and bundle current state to `0`; the refresh attempt and retry metrics explain why.
- **Validation and supported schedule range:** Watchdog validates raw CSV tables in addition to `go-gtfs` output because `go-gtfs` v1.1.1 falls back to UTC for invalid timezones, uses the first agency timezone for calendar parsing, skips malformed calendar/frequency rows, and cannot distinguish invalid or missing endpoint clocks from midnight after interpolation. Watchdog rejects missing calendar sources, bad dates/day flags, duplicate IDs or stop sequences, unresolved agency references, invalid/mixed feed timezones, invalid frequency fields, and missing/invalid first or last arrival/departure times. GTFS clocks above 24:00 are supported through seven service days (maximum `168:00:00`); larger offsets make the schedule unavailable rather than risking unbounded evaluation.
- **Calendars, overnight service, frequencies, and DST:** `calendar_dates.txt`-only feeds are supported. Exceptions override weekly rules. Prior service dates are checked through the compiled maximum overnight offset, so a `25:30:00` trip remains attributed to the preceding service day. Exact frequency windows retain headway and template-trip duration so genuine gaps are not marked active; `exact_times=0` (or blank) is conservatively active across the frequency span because GTFS does not define exact departures. Service-day instants use the agency timezone and GTFS's noon-minus-12-hours convention, keeping elapsed offsets monotonic across DST transitions.
- **Multiple feeds and agency attribution:** GTFS identifiers are feed-local, but a realtime source is not configured with a corresponding static source, so Watchdog cannot use source-feed identity to disambiguate equal route/trip IDs across static feeds. It retains every candidate owner instead of rejecting the complete static snapshot or silently choosing one. In agency mode, a sole blank `agency_id` is assigned only to the configured agency; otherwise explicit ownership must resolve. In server mode, route/trip candidates come from `routes.agency_id`, with blank route ownership accepted only for a single-agency feed. Multi-agency feeds must use one valid timezone as required by GTFS. Multiple feeds contributing to one agency must agree on that agency's timezone.
- **Count comparison limitation:** `realtime_vehicle_positions_count_gtfs_rt` and `oba_agency_active_vehicles_count` are observations from different processing paths, not an agreement check. Watchdog fetches and parses GTFS-RT itself, while the OBA count is the length of a vehicles-for-agency response. The paths can differ in observation time, refresh cadence, expiry, filtering, deduplication, and agency attribution. Their successful-fetch timestamps establish collection health only; they do not prove that both counts describe the same source snapshot. OBA returns per-vehicle update times, and GTFS-RT vehicle and feed-header timestamps are optional, but neither aggregate count has one guaranteed source-data timestamp. Do not retain vehicle IDs or reproduce application business logic solely to reconcile these gauges. Watchdog's role is to expose operational evidence; Maglev remains authoritative for its own calculations. If Watchdog contains a better authoritative calculation, move that logic into Maglev rather than validating Maglev against a duplicate implementation.
- **Vehicle state and source time:** Timestamp-only advances increment `gtfs_rt_vehicle_source_timestamp_advances_total` but do not move `gtfs_rt_vehicle_state_last_changed_timestamp_seconds` or increment the state-change counter. A stationary vehicle can therefore report fresh source timestamps without appearing semantically changed. Missing source timestamps remain missing. State comparison uses the raw `VehiclePosition` protobuf with only its timestamp removed.
- **Computed-speed retention:** An advancing valid vehicle source timestamp recomputes speed and updates `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds`. An identical timestamp with an identical position/state retains the last speed for `clamp(max(3 * collection interval, 2 * observed source interval), 60s, 300s)`. Missing or rolled-back timestamps, invalid positions, contradictory state at the same timestamp, expiry of that grace period, a successful `FULL_DATASET` departure, or feed removal retire speed, discrepancy, and computation-time series immediately.
- **Per-feed identity:** `feed` is the stable zero-based index in `gtfs_rt_feeds`; `feed_url` is sanitized. Vehicle IDs are namespaced by `(feed, vehicle_id)`, so equal IDs in different feeds remain distinct. Feed payload comparison sorts deterministic entity fingerprints, making entity order irrelevant, and excludes `FeedHeader` so a header timestamp alone cannot hide frozen content.
- **Fetch failures:** A failure sets `gtfs_rt_feed_fetch_success` to 0, removes dependent current-value feed series, and does not process the cached realtime snapshot as a new observation. Last-success and last-change timestamps remain historical evidence. Gate current charts on success when combining historical metrics.
- **Per-agency attribution in server-mode:** A server-scoped config entry (no `agency_id`) exposes one merged GTFS-RT feed covering several agencies. Watchdog walks that feed once per tick and compares the candidate agency sets for its `route_id` and `trip_id`. A one-agency intersection resolves the vehicle; for example, route candidates `{A,B}` plus trip candidates `{A}` resolve to A. Multiple remaining candidates stay ambiguous, and disjoint non-empty sets are a conflict. A single known route or trip remains sufficient when the other identifier is absent or unknown. OBA coverage is checked only after static ownership resolves and is never used to guess between candidates. Therefore, `agency_id` on an operational vehicle metric always means exactly one statically resolved agency reported by OBA.
- **Unattributed reason values:** `gtfs_rt_unattributed_vehicles_by_reason_count` uses these bounded values. Their sum for a server equals `gtfs_rt_unattributed_vehicles_count`:
  - `missing_vehicle_id`: static ownership may be known, but the entity has no usable vehicle ID and cannot safely produce per-vehicle state.
  - `missing_route_id`: neither route nor trip provides usable static ownership evidence, and `route_id` is absent.
  - `unknown_route_id`: the supplied route is absent from the current static attribution index and no trip candidate resolves it.
  - `agency_not_reported_by_oba`: static ownership resolves to exactly one agency, but that agency is not in the current server-mode `metrics.json` agency set.
  - `ambiguous_identifier`: the available route/trip evidence leaves more than one possible agency.
  - `route_trip_conflict`: route and trip have known but disjoint candidate agency sets.
  - **Potential associations are not assignments:** `gtfs_rt_unattributed_vehicle_potential_agency_associations_count` reports the agencies that remain plausible for unresolved vehicles. An ambiguous vehicle with potential agencies A and B increments both potential-agency series but increments the aggregate unattributed count only once. A missing-ID or not-reported-by-OBA vehicle may also have a potential agency. Missing/unknown identifiers with no static evidence have none. Never sum potential associations as a vehicle total; use them to identify which agencies need feed/config investigation. Raw vehicle, route, and trip IDs are intentionally excluded from Prometheus labels; static collision identifiers and potential agency owners are reported through the bounded logger/Sentry path.
- **Where unattributable vehicles land:** No vehicle disappears entirely, but the metric that accounts for it differs, and the three paths below do not add up to a single tidy identity — do not write an alert that assumes they do:
  - **Per-vehicle series** (`gtfs_rt_vehicle_source_timestamp_seconds`, `gtfs_rt_vehicle_state_last_changed_timestamp_seconds`, `gtfs_rt_vehicle_computed_speed`, `gtfs_rt_vehicle_speed_discrepancy_ratio`) and `gtfs_rt_tracked_vehicles_count` and `realtime_vehicle_positions_count_gtfs_rt` omit them. They are counted once in `gtfs_rt_unattributed_vehicles_count{server_name, server_url}` and exactly once in its reason breakdown. A persistently non-zero value means the static feeds do not cover everything the RT feed references, ownership is ambiguous/conflicting, the resolved agency is not reported by OBA, or the feed is emitting malformed entities.
  - **Attributable vehicles with no usable position** still expose state/source freshness when possible, but cannot expose computed-speed metrics. They also appear in `gtfs_rt_invalid_vehicle_coordinates`. This is why vehicle-count and quality metrics should not be expected to form one exact identity.
  - **The data-quality gauges in server-mode** (`gtfs_rt_invalid_vehicle_coordinates`, `gtfs_rt_stopped_out_of_bounds_vehicles`) count unattributed vehicles under the server-scoped series — the one with an empty `agency_id`/`agency_name`. Coordinate validity is judged *before* attribution precisely because the most malformed entities (no `TripDescriptor`, no position) are the ones attribution cannot place, and they are the ones these gauges exist to catch. So `sum by (server_url) (gtfs_rt_invalid_vehicle_coordinates)` is the true server-wide count, while the non-empty `agency_id` series give the breakdown:
  ```promql
  # server-wide, including unattributable vehicles
  sum by (server_url) (gtfs_rt_invalid_vehicle_coordinates)
  # just the vehicles that could not be placed with an agency
  gtfs_rt_invalid_vehicle_coordinates{agency_id=""}
  ```
  In agency-mode (an entry with an `agency_id`) only vehicles uniquely resolved to the configured agency reach these passes, so no empty-`agency_id`, aggregate unattributed, reason, or candidate-association series is published.
- **Bounding box accuracy depends on attribution:** `gtfs_rt_stopped_out_of_bounds_vehicles` is attributed per agency. Agency-mode uses the box calculated from its owned static snapshot and does not fall back to a server union. In server-mode, attributed vehicles use their agency box when available and otherwise use the server-wide union; unattributed vehicles use that server-scoped union box.
- **Series cleanup:** Successful `FULL_DATASET` snapshots retire per-vehicle state/source/speed series for entities that left the feed. The expiry routine also retires both in-memory state and Prometheus series. Differential feeds do not trigger absence cleanup.
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
| `oba_metrics_last_successful_fetch_timestamp_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url` | unix_timestamp | When Watchdog last decoded an OBA metrics response that contained the configured agency. Gate OBA metrics with this value before treating them as current. |
| `oba_realtime_trips_matched_count`   | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of matched realtime trips.                  |
| `oba_realtime_trips_unmatched_count` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of unmatched realtime trips.                |
| `oba_scheduled_trips_count`          | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of scheduled trips.                         |
| `oba_stops_matched_count`            | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of matched stops.                           |
| `oba_stops_unmatched_count`          | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of unmatched stops.                         |
| `oba_realtime_trip_match_ratio`      | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | ratio   | Ratio of matched realtime trips to total trips.    |
| `oba_stop_match_ratio`               | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | ratio   | Ratio of matched stops to total stops.             |
| `oba_time_since_last_update_seconds` | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | seconds | Time since last realtime update.                   |
| `oba_unmatched_stop_info`            | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`, `stop_id`, `stop_name`, `lat`, `lon` | N/A     | Presence marker (always 1) for unmatched physical stop locations from static GTFS. Each `(agency_id, stop_id)` resolves to one stop. |
| `oba_unmatched_stop_unresolved`      | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`                               | count   | Number of stop IDs OBA reported as unmatched that Watchdog could not resolve against its local GTFS bundle. |
| `oba_unmatched_stop_cluster_count`   | Gauge | `agency_id`, `agency_name`, `server_name`, `server_url`, `station_id`, `cluster_id`, `cluster_lat`, `cluster_lon` | count   | Number of unmatched physical stop locations grouped by station and S2 spatial cluster. |

**Interpretation Guide:**
- **Unmatched stop clusters:** Identify systemic coverage gaps. Each series is one `(station_id, cluster_id)` pair:
    - `cluster_id` is the S2 cell (level 13, ~850–1225 m spatial resolution) derived from the unmatched stops' **own** coordinates — never fetched per station, so no N+1 API calls.
    - `cluster_lat` / `cluster_lon` are the center of that S2 cell, so clusters can be plotted on a map or joined by coordinates without decoding the ID.
    - `station_id` is the root parent station ID when the stops belong to a station hierarchy, or `no_station` when they do not. A large station can span several S2 cells, yielding one series per `(station_id, cluster_id)` pair; group by `cluster_id` for spatial aggregation and by `station_id` for per-station totals.
- **Time since update:** If unusually high, real-time feed is stale.
- **`oba_unmatched_stop_info` retention:** Each `(agency_id, stop_id)` resolves to one stop in Watchdog's stored static snapshot. In server-mode that snapshot is the merged bundle; in agency-mode it is the owned agency-scoped snapshot. Its series is emitted at every scrape and pruned 24 hours after the stop last appeared unmatched. If its name or location changes, the old label set is retired immediately. A `1` means the stop was unmatched at some point in the last 24 hours; history is preserved by Prometheus itself — use range queries to separate days:
```promql
  # unmatched at some point during the last day
  max_over_time(oba_unmatched_stop_info{agency_id="unitrans"}[1d])
  # flapping signal (value changes during the day)
  changes(oba_unmatched_stop_info{agency_id="unitrans"}[1d])
  # daily recording rule to bucket unmatched physical locations per stop ID
  sum by (agency_id, stop_id) (max_over_time(oba_unmatched_stop_info[1d]))
```
- **Stop identity across feeds:** The merged bundle keeps the first occurrence of a duplicate `stop_id` and reports a warning when later feeds describe that ID at a different location. Runtime stop lookup and unmatched-stop metrics use that one retained stop.
- **Stop identity across snapshots:** A same-snapshot duplicate ID across configured feeds is a collision, but an existing `stop_id` may legitimately have corrected or relocated coordinates in a later static-feed snapshot. Watchdog exposes only the latest location; Prometheus retains samples from the previous label set as history after that old series becomes stale.
- **`oba_unmatched_stop_cluster_count` retention:** The value counts resolved unmatched stops. Cluster series follow the same 24h TTL as `oba_unmatched_stop_info` — a cluster's last reported count is retained until the cluster has not appeared for 24 hours, then the series is pruned. Use range queries to reconstruct historical cluster membership.
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
