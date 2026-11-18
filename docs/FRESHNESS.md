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

Static GTFS does not use the realtime window. There is no scheduled daily
download. Each collection tick decides, once per backend, how the bundle is kept
current:

1. Read the backend metadata hash, unless an earlier failure put the read in
   backoff. Only backends whose entries all share one static feed have a
   usable hash. A matching hash confirms the bundle current without a
   download. A differing hash starts a `backend_changed` download once the
   backend has reported it steadily for a short while, and the download is
   retried at most once an hour while the mismatch lasts (both described
   below).
2. A failed read backs off on its own, separately from the API ping backoff, so
   a broken metadata endpoint never stops the rest of collection. A transient
   failure (network error, timeout, 408, 429 or 5xx) backs off exponentially
   from the fetch interval up to 10 minutes. A backend with no usable hash
   (404, another 4xx, malformed JSON, a missing or invalid hash) is not asked
   again for an hour.
3. If the hash has not worked for a day, Watchdog downloads the bundle anyway
   with the `fallback` trigger, and repeats that daily while the hash stays
   unusable. Once the bundle has expired, a newer one is needed urgently, so the
   period shortens to 2 hours. In server mode one expired agency is enough to
   shorten the period for the whole backend, because the agencies share one
   feed. A byte-identical download confirms the bundle without re-parsing it.

   The day is counted from the last time the hash worked, whether it matched
   or not. The clock starts when Watchdog first sees the backend and restarts
   at each fallback start. A successful download started by another trigger
   does not restart it.

Startup, a configuration change and an agency-coverage mismatch still start a
download at once.

The hash check in step 1 reads `/api/v2/metadata.json`. It applies only to
backends whose configured entries all reference the same single static feed.
If the backend's `staticGtfsHash` differs from the
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
bundle is refreshed like any other change, except for the quick rollback
described below.

Each stable mismatch starts one campaign. If the backend keeps reporting the
same hash afterwards, Watchdog logs a warning that the backend and the
configured feed still disagree, and retries the campaign at most once an hour
while the mismatch lasts, which recovers a feed that was corrected without a
redeploy or a download that failed. The hourly limit holds when the wait
starts again for the same backend hash, local bundle and configuration, for
example when a node stuck on an old bundle answers behind a load balancer, or
after a metadata outage. It ends once the backend has reported another hash
for 10 minutes, so a rollback after a deployment that ran that long refreshes
right away. A rollback sooner than that, to a bundle that already started a
campaign within the last hour, cannot be told apart from a stale node behind a
load balancer, so it waits for the hourly retry (at most one hour). The shared metadata request is skipped
while every entry for a backend is in collection backoff, and while the
metadata read itself is in backoff. It resumes once an entry is ready and the
metadata backoff has passed. A skipped request leaves the last hash-match observation intact. Neither skip delays the fallback download, which reads the feed URL rather than the backend.

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
unchanged. Backends with different static feed URLs across agency entries have no usable hash, so they rely on the fallback download. The existing agency coverage
comparison continues to report coverage and filter agency processing; it does
not start static refreshes.

| State | Definition |
| --- | --- |
| Current | The bundle is usable, the latest refresh succeeded, and within 26 hours it was downloaded or confirmed by a matching backend hash. |
| Retained stale | A complete, unexpired bundle remains, but refresh is old or the latest attempt failed. |
| Expired and unusable | Agency-local today is later than the latest service end date. |
| Missing | No complete bundle exists. |

Each entry consumes one upstream-merged static artifact. Startup, changed configuration, a backend hash change, an agency-coverage mismatch and the fallback start managed per-entry campaigns; a changed or removed entry cancels its stale campaign. The compressed artifact is cached only for the campaign. Publication waits until it parses, compiles, and validates. Duplicate primary IDs reject the artifact with failure reason `duplicate_id`. A failed campaign preserves the previous complete bundle.

Normal retries run for at most 12 hours using delays of 5 minutes, 10 minutes, 20 minutes, 40 minutes, 80 minutes, then at most 2 hours. HTTP 429 can extend a delay through `Retry-After`.

A feed that is clearly broken enters conservative mode, without any change to user configuration. That happens after:

- an invalid URL or a 401/403 response, in any campaign;
- the same invalid archive, agency-discovery or schedule content (identical content hash) 3 times in a row, in any campaign;
- a 404/410 at the end of one failed fallback campaign;
- 7 failed fallback campaigns of any other kind (network errors, 408, 5xx, unknown).

The last two rules count failed campaigns, and only fallback campaigns count toward them (see `staticTriggerPolicies`), because a one-off trigger such as startup or a configuration change says nothing about how a feed behaves over time. The first two rules look at single attempts, so they apply whatever started the campaign. Once a feed is conservative, every campaign ends after its first failed attempt, so a fallback download gives it a single try. Any successful attempt clears the state and restores the normal retry loop.

When the agency-local date passes a bundle's latest service date, the bundle has expired. Watchdog then reports the schedule as unknown (`gtfs_schedule_available` 0), skips the vehicle pass, which attributes vehicles and checks bounds with the bundle, and deletes the gauges that pass writes: `realtime_vehicle_positions_count_gtfs_rt`, `gtfs_rt_tracked_vehicles_count`, `gtfs_rt_invalid_vehicle_coordinates`, `gtfs_rt_stopped_out_of_bounds_vehicles`, `gtfs_rt_vehicle_computed_speed`, `gtfs_rt_vehicle_speed_discrepancy_ratio`, `gtfs_rt_vehicle_source_timestamp_seconds`, `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds` and `gtfs_rt_vehicle_state_last_changed_timestamp_seconds`. Counters are kept and simply stop increasing. In server mode the pass is shared by the merged bundle, so it is skipped only once every reported agency has expired, while the faster fallback starts as soon as one agency has expired. API ping, OBA API metrics, GTFS-RT fetch status and the expiration gauges (`gtfs_bundle_days_until_earliest_expiration` and `gtfs_bundle_days_until_latest_expiration`) keep reporting. Sentry receives one `fatal` event when a bundle expires, tagged `cause=refresh_failing` (the last refresh failed) or `cause=no_newer_bundle` (the last refresh succeeded, so the agency has not published a newer bundle), and one `info` event when it recovers.

## Computed Vehicle Speed

Computed speed has its own validity age, exposed by `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds`. An unchanged valid source timestamp and identical vehicle state retain speed for:

```text
clamp(max(3 * collection interval, 2 * observed source interval), 60s, 300s)
```

Missing or rolled-back timestamps, invalid positions, contradictory same-timestamp state, vehicle departure from a full snapshot, and removed feeds invalidate the speed immediately.

Raw feed transport metrics remain meaningful without static GTFS. Static-derived attribution, schedule, geographic checks, inventory, and per-agency GTFS-RT metrics require usable static data.
