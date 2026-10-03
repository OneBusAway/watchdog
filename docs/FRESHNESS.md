# Observation Freshness Contract

Watchdog exposes observations through Prometheus. A recent Prometheus sample means `/metrics` was scraped recently; it does not mean the underlying source was collected recently.

## Common Realtime Window

Clients must calculate the current-value freshness window from:

```promql
clamp_max(clamp_min(watchdog_collection_interval_seconds * 3, 60), 300)
```

This allows three collection cycles, never less than 60 seconds and never more than five minutes.

Current-value queries must also require a recent `watchdog_collection_last_completed_timestamp_seconds`. This distinguishes a healthy metrics endpoint from a stalled collection loop.

Source metrics require their authoritative successful-fetch timestamp:

| Source | Freshness metric |
| --- | --- |
| OBA `metrics.json` | `oba_metrics_last_successful_fetch_timestamp_seconds` |
| OBA vehicles-for-agency | `oba_vehicles_last_successful_fetch_timestamp_seconds` |
| Complete GTFS-RT feed set | `gtfs_rt_last_successful_fetch_timestamp_seconds` |
| Individual GTFS-RT feed | `gtfs_rt_feed_last_successful_fetch_timestamp_seconds` and `gtfs_rt_feed_fetch_success` |

A successful zero remains `0`. A failed or old collection must produce an absent result so charts render a gap or `No data`.

## Static GTFS

Static GTFS refreshes daily and does not use the realtime window.

| State | Definition |
| --- | --- |
| Current | Complete refresh succeeded within 26 hours and the bundle is usable. |
| Retained stale | A complete, unexpired bundle remains, but refresh is old or the latest attempt failed. |
| Expired and unusable | Agency-local today is later than the latest service end date. |
| Missing | No complete bundle exists. |

Static refresh is atomic across all configured feeds in one configuration entry. Startup, changed configuration, and the daily boundary start managed per-entry campaigns; a changed or removed entry cancels its stale campaign. Successful compressed artifacts are cached only for the campaign, retries fetch only failed feeds, and publication waits until the complete set parses, compiles, and validates. A failed campaign preserves the previous complete bundle.

Normal retries run for at most 12 hours using delays of 5 minutes, 10 minutes, 20 minutes, 40 minutes, 80 minutes, then at most 2 hours. HTTP 429 can extend a delay through `Retry-After`. Repeated or deterministic failures enter conservative mode without modifying user configuration: the feed receives one probe at each daily boundary, and any successful probe immediately restores normal complete-campaign behavior.

## Computed Vehicle Speed

Computed speed has its own validity age, exposed by `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds`. An unchanged valid source timestamp and identical vehicle state retain speed for:

```text
clamp(max(3 * collection interval, 2 * observed source interval), 60s, 300s)
```

Missing or rolled-back timestamps, invalid positions, contradictory same-timestamp state, vehicle departure from a full snapshot, and removed feeds invalidate the speed immediately.

Raw feed transport metrics remain meaningful without static GTFS. Static-derived attribution, schedule, geographic checks, inventory, and per-agency GTFS-RT metrics require usable static data.
