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
| Accepted GTFS-RT snapshot | `gtfs_rt_last_successful_fetch_timestamp_seconds` |
| Individual GTFS-RT feed | `gtfs_rt_feed_last_successful_fetch_timestamp_seconds` and `gtfs_rt_feed_fetch_success` |

A successful zero remains `0`. A failed or old collection must produce an absent result so charts render a gap or `No data`.
Current-value panels must match each source's freshness timestamp (and, for GTFS-RT feeds, its latest success status), as well as the recent collection-loop completion timestamp. Aggregate GTFS-RT values require every configured feed in that scope to be successful and fresh. Per-feed vehicle values must match the same `agency_id`, `agency_name`, `server_name`, `server_url`, and `feed` labels in both feed metrics. Do not use payload-change age as a freshness gate: a successful fetch with an unchanged payload is still a current observation.

## Static GTFS

Static GTFS refreshes daily and does not use the realtime window.

For backends whose configured entries all reference the same single static feed,
each collection tick also checks
`/api/v2/metadata.json`. If the backend's `staticGtfsHash` differs from the
SHA-256 of Watchdog's published zip, Watchdog treats it as a possible bundle
change. Because the hash a deployment reports can briefly disagree with the
feed URL while a redeploy is in progress, Watchdog waits until the backend has
reported the same mismatching hash for at least 90 seconds and at least 3
collection ticks before starting a `backend_changed` refresh campaign. A
different hash, a change in Watchdog's own published bundle, a static
configuration change, or a gap longer than 90 seconds or 3 fetch intervals
(whichever is longer) starts the wait again. Ticks without a hash neither end
nor extend it. Nodes alternating between bundle versions behind a load
balancer therefore rarely start a campaign, and a rollback to an earlier
bundle is refreshed like any other change.

Each stable mismatch starts one campaign. If the backend keeps reporting the
same hash afterwards, Watchdog logs a warning that the backend and the
configured feed still disagree, and retries the campaign at most once an hour
while the mismatch lasts, which recovers a feed that was corrected without a
redeploy or a download that failed. The hourly limit holds when the wait
starts again for the same backend hash, local bundle and configuration, for
example when a node stuck on an old bundle answers behind a load balancer, or
after a metadata outage. It ends once the backend has reported another hash
for 10 minutes, so a rollback after a deployment that ran that long refreshes
right away, and a quicker rollback waits for the hourly retry. Daily refreshes
continue independently. The shared metadata request is skipped
when every entry for a backend is in collection backoff; it resumes when any
entry is ready. A skipped request leaves the last hash-match observation intact.

Malformed metadata and unexpected request failures are logged at debug level,
backend-triggered campaign starts at info level, and a mismatch that outlasts
its campaign at warn level, once per campaign. Missing hashes and HTTP
404/503 responses remain quiet.

`gtfs_static_backend_hash_match` reports 1 for a match and 0 for a mismatch.
It is omitted until a local snapshot has been published. A failed refresh
preserves the hash of the snapshot still in use. Entries sharing a backend
and metric labels produce one combined result, which is unknown if any local
snapshot hash is unavailable.
A persistent 0 can mean the configured feed URL differs from the backend's
bundle source, or the backend has not loaded the latest feed. Missing or failed
metadata responses omit the gauge and leave normal collection and API backoff
unchanged. Backends with different static feed URLs across agency entries retain daily refreshes only. The existing agency coverage
comparison continues to report coverage and filter agency processing; it does
not start static refreshes.

| State | Definition |
| --- | --- |
| Current | Complete refresh succeeded within 26 hours and the bundle is usable. |
| Retained stale | A complete, unexpired bundle remains, but refresh is old or the latest attempt failed. |
| Expired and unusable | Agency-local today is later than the latest service end date. |
| Missing | No complete bundle exists. |

Each entry consumes one upstream-merged static artifact. Startup, changed configuration, and the daily boundary start managed per-entry campaigns; a changed or removed entry cancels its stale campaign. The compressed artifact is cached only for the campaign. Publication waits until it parses, compiles, and validates. Duplicate primary IDs reject the artifact with failure reason `duplicate_id`. A failed campaign preserves the previous complete bundle.

Normal retries run for at most 12 hours using delays of 5 minutes, 10 minutes, 20 minutes, 40 minutes, 80 minutes, then at most 2 hours. HTTP 429 can extend a delay through `Retry-After`. Repeated or deterministic failures enter conservative mode without modifying user configuration: the feed receives one probe at each daily boundary, and any successful probe immediately restores normal complete-campaign behavior.

## Computed Vehicle Speed

Computed speed has its own validity age, exposed by `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds`. An unchanged valid source timestamp and identical vehicle state retain speed for:

```text
clamp(max(3 * collection interval, 2 * observed source interval), 60s, 300s)
```

Missing or rolled-back timestamps, invalid positions, contradictory same-timestamp state, vehicle departure from a full snapshot, and removed feeds invalidate the speed immediately.

Raw feed transport metrics remain meaningful without static GTFS. Static-derived attribution, schedule, geographic checks, inventory, and per-agency GTFS-RT metrics require usable static data.
