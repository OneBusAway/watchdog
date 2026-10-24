# ADR 0001: Durable Historical Investigation Storage

**Status:** Accepted for the initial single-instance design; implementation TODOs are listed below.  
**Scope:** Watchdog entity-level history, Prometheus aggregates, PostgreSQL persistence, and OBACloud investigation views.  
**Related issues:** [#156](https://github.com/OneBusAway/watchdog/issues/156), [#157](https://github.com/OneBusAway/watchdog/issues/157)

## Context

Watchdog collects the current OBA metrics and GTFS-Realtime feeds on a 30-second interval by default. It exports Prometheus metrics and keeps the latest vehicle and unmatched-stop tracking state in process memory.

The OBA `/api/where/metrics.json` endpoint is a current snapshot. When it changes, previously unmatched stop or trip IDs are no longer available from that endpoint. In-memory state is lost when Watchdog restarts. Aggregate metrics can show a feed or agency problem, but do not identify the affected entity or preserve the historical metadata needed for later investigation.

The goal is to provide durable, queryable investigation evidence without retaining every entity from every poll or adding unbounded identifiers as Prometheus labels.

### Why retention needs care

At one observation every 30 seconds, each vehicle can produce 2,880 observations per day:

| Calculation | 100 vehicles | 1,000 vehicles |
| --- | ---: | ---: |
| Observations per vehicle per day | 2,880 | 2,880 |
| Vehicle observations per day | 288,000 | 2,880,000 |
| Vehicle observations in 30 days | 8,640,000 | 86,400,000 |

That is for a single wide database row per vehicle per poll. Storing each field as a separate row would multiply the row count.

Prometheus scrapes the current production Watchdog target every 30 seconds. Watchdog can expose up to five vehicle-labelled metric families: source timestamp, semantic state-change timestamp, computed speed, speed discrepancy, and last speed-calculation time. With 100 vehicles and all five metrics present, that is up to 500 active series:

| Calculation | 100 vehicles, five series each |
| --- | ---: |
| Active time series | Up to 500 |
| Samples per series per day | 2,880 |
| Samples per day | Up to 1,440,000 |
| Samples in 30 days | Up to 43,200,000 |

These are illustrative upper bounds: source timestamps are optional, and speed-related series are emitted only when enough valid data is available. The figures scale linearly with the number of vehicles. They are sample counts, not a precise memory or disk estimate; Prometheus also uses memory and index space for each active series, and PostgreSQL row and index sizes depend on the final schema.

The point is not that 100 vehicles alone overwhelm either database. The risk is making every vehicle and every poll part of the durable history as the monitored fleet grows. We should retain detail only when there is an investigation reason to keep it.

## Decision

### Prometheus stores aggregates

Prometheus remains the source for feed-, agency-, and fleet-level health and trend metrics. It will not receive raw vehicle, stop, or trip IDs as labels.

For vehicle source freshness, Watchdog will inspect each vehicle in the current successful feed response and emit low-cardinality summaries by server, agency, and feed:

- p95 source-timestamp age;
- oldest source-timestamp age;
- number of source timestamps beyond the freshness threshold;
- number of vehicle records without a source timestamp;
- reported-speed summaries and invalid-speed count, once their definitions are finalized.

The source-age p95 is calculated from the vehicles in each current successful feed snapshot. It is not a cumulative p95: Prometheus stores successive aggregate values over time so OBACloud can chart the p95 history. The current-feed p95 calculation may use temporary per-poll values, but it does not retain per-vehicle Prometheus series.

Prometheus continues to own feed availability, collection freshness, OBA processing freshness, schedule state, and the other existing aggregate alert signals.

### Watchdog keeps minimal per-vehicle state in memory

For the initial single-instance deployment, Watchdog will retain the existing `VehicleLastSeen` state needed to calculate timestamp-advance counters, semantic state-change counters, computed speed, and speed-calculation age. The state is one latest record per active vehicle, not a historical sample for every poll.

This deliberately keeps those aggregate calculations while removing raw per-vehicle Prometheus series. It does not add normal per-vehicle writes to PostgreSQL. The map is process-local and resets on restart; metrics that depend on previous observations warm up again from new feed data. A source-age value can be calculated from the current feed immediately. Speed calculation needs valid successive positions/timestamps.

If the memory footprint becomes significant, profile a production-like feed and reconsider which stateful aggregates are worth keeping. Do not assume the per-vehicle state map has zero cost; it is O(active vehicle count), in addition to the current realtime feed snapshot already held by Watchdog.

### PostgreSQL stores abnormal episodes and evidence

PostgreSQL is the durable investigation store. It stores only records operators may need to investigate later, such as:

- unmatched-stop episodes and versioned stop metadata;
- unmatched-trip episodes from OBA's authoritative unmatched-trip IDs;
- vehicle stale-source-timestamp episodes;
- invalid-coordinate episodes;
- stopped-out-of-bounds episodes, including the bounding-box version used;
- reported-speed anomaly episodes only after a Watchdog-side plausibility threshold is approved.

For a vehicle anomaly, store evidence when the episode opens, when it resolves, and when materially relevant evidence changes. Do not insert an ordinary row for each vehicle every 30 seconds. Repeated polls with no meaningful change during the same episode do not create repeated evidence rows.

PostgreSQL is optional for Watchdog's monitoring loop. If it is unavailable, Watchdog continues collecting and exporting Prometheus metrics. Entity-level investigations are incomplete during the outage. PostgreSQL outage records, an availability metric, and alerts are deferred and are not part of the initial migration.

## Vehicle-staleness meaning

The current OBACloud stale-vehicle threshold is:

```text
source timestamp age > clamp(5 × collection interval, 60 seconds, 300 seconds)
```

A vehicle whose source timestamp exceeds this threshold is a **stale GTFS-Realtime observation**. This does not prove the physical vehicle has broken down or stopped reporting. A feed may retain an old entity after a trip ends, and `VehiclePosition.trip` is optional or may be incomplete.

- Open an episode when a successful vehicle feed observation first exceeds the threshold.
- Close it on the first later successful observation with a fresh timestamp.
- If the vehicle is absent from a later successful `FULL_DATASET` snapshot, close it as **no longer present in feed**, not as recovered.
- Do not close it because of a failed fetch or absence from a `DIFFERENTIAL` response.
- The episode times are Watchdog observation times, not exact real-world onset/recovery times.

GTFS-Realtime describes `FULL_DATASET` as a snapshot of the producer's current realtime entities. It is not a roster of every physical vehicle. A vehicle leaving such a snapshot can be entirely normal. `DIFFERENTIAL` is updates-only; its omission of a vehicle says nothing about that vehicle. The specification currently describes differential behavior as unsupported/unspecified, and Watchdog must not infer absence from it.

### Minimal historical trip-schedule context

The schedule context is limited to what is needed to annotate a known trip as **expected active**, **expected inactive**, or **unknown** at observation time:

- trip ID and service ID;
- the trip's scheduled final arrival/departure time;
- service calendar and calendar-date exceptions;
- agency timezone;
- frequency window and trip start-time context where needed to identify a trip instance.

Build this compact per-trip time-window index in memory at startup and after each successfully published static GTFS refresh. The current schedule snapshot discards trip IDs, so Watchdog must retain a per-trip lookup for this purpose. At incident time, persist the calculated status and the GTFS snapshot ID used, along with the reported trip descriptor needed to explain the result.

Do not retain full `stop_times.txt` or the whole GTFS archive solely for this classification. Do not persist the complete trip schedule in PostgreSQL initially. If historical results later need recalculation under new rules, add a versioned trip-schedule table; keep this as a code and plan TODO. Trips with no usable static match, new/duplicated/unscheduled trips, or insufficient trip-instance details are `unknown`. This schedule context is for vehicle investigation; it must not be used to invent unmatched-trip failure reasons or to infer fields OBA did not provide.

## Speed data and validation

GTFS-Realtime's optional `Position.speed` is the momentary speed measured by the vehicle, in meters per second. Static GTFS has no legal road speed-limit field, and the OBA/Maglev vehicle-status endpoints reviewed do not expose road speed limits. External map data such as OpenStreetMap `maxspeed` would require separate map matching and handling of vehicle-specific, conditional, directional, and unit-specific values; it is outside this design's initial scope.

Watchdog's current computed speed is derived from successive positions and source timestamps. The discrepancy ratio compares that result with the source-reported speed. Those calculations require previous per-vehicle state. They are not a legal speed-limit test.

The current direction is to:

- treat source-reported speed as the primary value;
- treat missing speed as unknown, since the field is optional;
- treat zero as valid;
- treat negative and non-finite values as invalid under Watchdog's validation policy;
- publish only low-cardinality speed summaries to Prometheus;
- persist an individual vehicle's speed evidence only after a Watchdog-side plausibility threshold has been defined.

The numeric threshold is **not decided**. `250 m/s` is `900 km/h`, so it would be a very loose sanity ceiling, not a useful road-speed limit. Historical speed distributions should be inspected before selecting a threshold. A Prometheus alert threshold and a Watchdog persistence threshold serve different purposes: Prometheus can alert on an aggregate value; Watchdog needs its own threshold to decide which vehicle details to save in PostgreSQL.

Before selecting a plausibility threshold, determine and record the maximum reported speed observed in the available historical GTFS-Realtime data. That maximum informs the analysis; it is not automatically the chosen threshold.

## Normalized PostgreSQL schema direction

This is the logical schema direction; final DDL, exact types, and indexes remain implementation work. The schema separates identities, versioned static context, observations, episodes, and evidence.

### Identity and versioned context

- `server`, `agency`, `vehicle_position_endpoint` — deployment and endpoint identities. The endpoint has a generated database primary key and a normalized, credential-free vehicle-position URL; enforce `UNIQUE(server_id, normalized_vehicle_position_url)`. The zero-based config index is not persistent identity. Identical URLs on one server identify the same endpoint; a changed URL creates a new endpoint row. Incidents reference the endpoint actually used. Watchdog currently fetches `vehicle_position_url`; `trip_update_url` is not part of this identity.
- `vehicle` — unique `(vehicle_position_endpoint_id, external_vehicle_id)` identity. Normal vehicle samples are not stored as history here.
- `gtfs_snapshot`, `gtfs_snapshot_source`, `stop`, `stop_version`, `trip` — static identifiers and metadata versions needed to interpret an event later. A snapshot records its server, publication time, combined content hash, and the source static-feed hashes; it identifies the static generation but does not contain the GTFS archive.
- No PostgreSQL trip-schedule-window or full service-calendar projection initially. Those windows are compiled into Watchdog memory and the incident stores the computed schedule status plus its GTFS snapshot reference.
- `bounding_box_version` — bounds, scope (server/agency), source GTFS snapshot, and effective version.
- `cluster_scheme`, `s2_cell`, `stop_cluster_assignment` — versioned stop-to-station/cell assignments. Cluster counts are derived from stop episodes rather than stored as duplicate count events.

### Observation and episode tables

- `oba_metrics_snapshot` and `oba_metrics_snapshot_agency` — successful OBA observation time, upstream `currentTime`, and agencies covered by that response.
- `gtfs_rt_feed_observation` — vehicle-position endpoint, Watchdog observation time, source header timestamp when present, fetch result, incrementality (`FULL_DATASET`/`DIFFERENTIAL`), and payload digest, recorded only when needed as episode evidence rather than on every poll.
- `unmatched_stop_episode` and `unmatched_trip_episode` — entity identity plus opening and resolution snapshot references.
- `unmatched_stop_episode_metadata` — links an episode to each GTFS stop-metadata version that applied while it was unmatched.
- `vehicle_anomaly_type`, `vehicle_anomaly_rule_version`, `vehicle_anomaly_episode`, and `vehicle_anomaly_evidence` — incident type/rule, open/closed episode, and opening/resolution evidence.
- `vehicle_anomaly_episode_evidence` — normalized link between an episode and one or more evidence records.

Cluster counts are derived from stop episodes and versioned stop-to-cluster assignments rather than stored as duplicate count events. IDs and values remain scalar columns; independent facts use separate rows and foreign keys, not arrays of IDs or copied metadata blobs.

The design follows 1NF–4NF: atomic fields; attributes depending on complete keys; shared metadata in reference/version tables; and independent one-to-many or many-to-many facts in separate tables/relations.

Closed episodes and their evidence are retained for 90 days. Open episodes and context they reference are retained until resolution. Historical GTFS context should be minimal and retained only while referenced by an open or in-retention episode. No historical Prometheus samples are backfilled; PostgreSQL history starts at rollout.

### Deferred: vehicle-feed absence history

Vehicle absence is not persisted as a separate episode in the initial schema. Its initial meaning is only that an entity present in a prior successful `FULL_DATASET` vehicle-position response is absent from a later successful `FULL_DATASET` response. It does not mean the vehicle failed, stopped operating, or has a problem; it may simply have completed its trip or been removed by the producer. Never infer absence from a failed fetch or `DIFFERENTIAL` response.

If this history is later requested, store the endpoint, external vehicle ID, last-present observation, first-absent complete-snapshot observation, and optional first reappearance observation. Record transitions rather than every poll. A candidate bounded Prometheus metric is `gtfs_rt_vehicle_feed_absence_transitions_total{server,agency,endpoint}`, incremented once when a vehicle changes from present to absent across comparable successful complete snapshots; the first snapshot after startup only establishes the baseline. This counts observed feed-list transitions, not failures or alerts by itself. The existing `gtfs_rt_feed_vehicle_entities` gauge remains the initial aggregate signal. Do not add a vehicle-ID Prometheus label.

## Prometheus metrics and dashboards

### Keep in Prometheus

Keep aggregate health and alert inputs, including:

- source-age p95/oldest/stale count;
- `gtfs_rt_vehicle_source_timestamp_advances_total` and `gtfs_rt_vehicle_state_changes_total`;
- current attributed-vehicle count;
- source/feed freshness;
- invalid-coordinate and stopped-out-of-bounds counts;
- OBA agency/feed counts.

The source-age p95 is computed from the current successful snapshot. The timestamp-advance and state-change counters use the in-memory `VehicleLastSeen` state. These metrics have bounded labels; the counters do not have `vehicle_id` labels.

### Remove raw per-vehicle Prometheus series

After aggregate replacements and investigation queries are available, stop exporting raw per-vehicle series:

- `gtfs_rt_vehicle_source_timestamp_seconds`;
- `gtfs_rt_vehicle_state_last_changed_timestamp_seconds`;
- `gtfs_rt_vehicle_computed_speed`;
- `gtfs_rt_vehicle_speed_discrepancy_ratio`;
- `gtfs_rt_vehicle_speed_last_computed_timestamp_seconds`.

OBACloud's source-age table should use low-cardinality p95/oldest/stale gauges. Its speed-age chart can keep its existing p95/oldest meaning using the in-memory state and low-cardinality gauges. The source-timestamp-advances rate and state-change counter remain aggregate Prometheus metrics.

The current Watchdog Grafana speed-validation panel queries per-vehicle computed speed and discrepancy. Replace it with aggregate summaries and a link to PostgreSQL vehicle anomaly evidence; do not retain a series per vehicle for that panel.

No current production alert rule references the raw per-vehicle metric families. Existing feed-level alerts remain the paging mechanism; individual stale observations and vehicle anomalies are for investigation, not per-vehicle PagerDuty alerts.

## Deferred: PostgreSQL availability and outage gaps

PostgreSQL is optional for core collection. Watchdog must continue collecting feeds, serving `/metrics`, and exporting aggregate metrics when the investigation database is unavailable. PostgreSQL outage records, health metrics, and alerts are not part of the initial migration.

Future proposal: `watchdog_investigation_postgres_available` (`1` available, `0` unavailable). Detect failures using periodic short-timeout checks and actual write errors. Retain the first failure time in process memory, then write an outage interval after recovery. If Watchdog also restarts during an outage, the exact start time may be lost; Prometheus will still show the availability metric while Watchdog remains scrapeable.

If implemented later, add a Prometheus alert rule for this metric. Whether it only appears in Prometheus/Alertmanager or gets an explicit low-urgency PagerDuty route remains open; most warning alerts are intentionally blackholed today.

## Deployment decision

Run one Watchdog instance initially. Its in-memory `VehicleLastSeen` map gives stateful aggregate metrics a single owner. A restart resets that cache; counters reset normally and computed speed needs new valid successive observations.

Render does not copy process memory between instances. If Watchdog is later scaled horizontally, each instance would otherwise run its own polling loop with separate state, duplicate collection, and potentially inconsistent metrics/events. Render's persistent disks are single-instance and are not shared memory or a replica-coordination mechanism.

**Future multi-instance requirement:** add a design document, update Watchdog `CLAUDE.md` to warn against multi-instance collection before that design is implemented, and use a PostgreSQL lease so one instance owns each server/feed at a time. Make event writes idempotent. When PostgreSQL is down, replicas can only hold the first observed outage time locally. After recovery, concurrent outage reports should merge so the persisted start is the earliest time any replica detected the outage. If every replica restarts before recovery, PostgreSQL cannot receive the outage start while it is unavailable; preserving that exact time would require an independent durable source.

## Investigation questions this data can answer

- Which stops/trips had unmatched episodes during a selected time range, and what metadata version applied?
- Which vehicle incidents occurred, where, and when were they detected/resolved?
- Which bounding-box version was used for a stopped-out-of-bounds incident?
- Was a known trip expected active, inactive, or unknown when a stale vehicle observation was recorded?
- What were the source-age p95/oldest/stale counts and speed-calculation-age p95/oldest for a feed/agency over time?

Vehicle-feed absence history and PostgreSQL-outage history are deferred questions; see the sections above.

## Decision log

| Question | Decision | Reason |
| --- | --- | --- |
| Durable entity history store | PostgreSQL | Supports filtered, paginated historical investigations without putting IDs in Prometheus labels. |
| Prometheus role | Feed/agency/fleet aggregates and alerting | Retains trends and alert signals without normal per-vehicle samples. |
| Persist every 30-second vehicle sample? | No | Would grow as vehicles × polls × metrics. Store anomaly opening/resolution evidence and materially meaningful changes only. |
| Evidence cadence during an open vehicle anomaly? | Opening, resolution, and materially meaningful evidence changes only | Repeated identical polls do not add rows. |
| Persistent vehicle-position feed identity | Generated endpoint PK, unique normalized credential-free URL per server | Config order must not affect identity; URL changes create a new endpoint identity; no OBACloud feed ID or URL alias mapping is required. |
| Keep vehicle state in Watchdog memory? | Yes, one latest state per active vehicle for stateful aggregate calculations | Preserves OBACloud summaries without exporting per-ID series or writing normal state to PostgreSQL. |
| Current vehicle state in PostgreSQL? | No | Single-instance deployment; PostgreSQL holds abnormal episodes/evidence only. |
| Trip context | Compile compact per-trip windows in memory on startup and successful static refresh; persist result and snapshot reference | Gives operators context without persisting a full schedule projection. Add a code/plan TODO for a versioned database schedule table if historical recalculation becomes necessary. |
| Vehicle feed absence | Informational, not a failure | `FULL_DATASET` absence means no longer listed; `DIFFERENTIAL` absence is ignored. |
| Out-of-bounds condition | Only when `STOPPED_AT` | Preserves existing Watchdog semantics. |
| Stale source threshold | `clamp(5 × collection interval, 60s, 300s)` | Matches current OBACloud freshness table. |
| Stale episode resolution | One successful fresh observation closes the episode | Failed collection cannot confirm recovery. |
| Speed source | Trust optional reported GTFS-RT speed; validate plausibility | No legal road speed limit exists in GTFS or the reviewed OBA endpoints. |
| Speed plausibility threshold | Not selected | Inspect historical values before setting it; add Watchdog configuration if anomalous vehicles will be persisted. |
| Retention | 90 days for closed events; open episodes retained | Initial bounded period; revisit after measuring actual traffic and event volume. |
| Historical Prometheus backfill | No; PostgreSQL history starts at rollout | Existing samples lack the complete event semantics/metadata needed by the new store. |
| Vehicle-feed absence persistence | Deferred; no separate absence rows or new absence metric initially | Absence from a complete snapshot does not establish vehicle failure. If reconsidered, persist transition evidence and use only a bounded aggregate metric (proposal above). |
| PostgreSQL outages | Deferred; no outage table, health metric, or alert in the initial migration | Investigation persistence must not stop feed monitoring. |
| PostgreSQL driver and migration tool | `pgx` v5 (`pgxpool`) driver, `goose` v3 SQL migrations, `sqlc` query generation | `pgx` is the native PostgreSQL driver and the one `sqlc`'s `pgx/v5` output targets. `goose` migrations are plain SQL files with Up and Down sections, which `sqlc` reads directly as its schema; they are embedded in the binary and applied at startup under a PostgreSQL advisory lock, so a future second instance cannot apply them twice. `golang-migrate` was the main alternative; it needs two files per migration and offers nothing this design needs that `goose` lacks. |
| Historical maximum reported speed | Must be measured before selecting a plausibility threshold | Record the maximum from historical GTFS-Realtime data; do not assume the maximum itself is the threshold. |
| Watchdog instances | One initially | Avoid duplicate polling and split in-memory state. |

## Remaining work

- [x] Finalize PostgreSQL DDL, indexes, endpoint URL uniqueness, and episode constraints; choose the PostgreSQL driver and migration tool. Implemented in `internal/db/migrations/00001_create_investigation_schema.sql`.
- [ ] Implement retention cleanup.
- [ ] Implement snapshot records and unmatched stop/trip episodes with versioned stop metadata and compact trip-schedule context.
- [ ] Implement vehicle stale-source, invalid-coordinate, and stopped-out-of-bounds episodes with opening/resolution evidence.
- [ ] Extend the in-memory schedule index to retain compact per-trip time windows at startup and successful static GTFS refresh; add a code comment about a future versioned PostgreSQL schedule projection if historical recalculation is required.
- [ ] Define low-cardinality aggregate metric names and update OBACloud and bundled Grafana queries before dropping raw per-vehicle Prometheus series.
- [ ] Inspect historical GTFS-Realtime reported-speed data and determine the maximum ever reported speed before choosing a plausibility threshold. Expose a Watchdog CLI/config option if individual speed outliers should be saved in PostgreSQL.
- [ ] Revisit whether vehicle-feed absence history is needed. If selected, persist only complete-snapshot presence-to-absence transitions and consider the bounded aggregate metric described above; absence must not be presented as a vehicle failure.
- [ ] Keep PostgreSQL availability metrics, outage records, and alerts deferred until a later scope decision; decide whether any future alert should notify PagerDuty.
- [x] Compare PostgreSQL driver and migration options (selected: `pgx` v5, `goose` v3, `sqlc`; see the decision log). Maglev uses `database/sql` with SQLite drivers and `sqlc` for query-code generation; it embeds `schema.sql` and executes statements split on `-- migrate` at startup. This is a custom SQLite schema runner, not a versioned migration framework. Maglev has no PostgreSQL runtime driver; `pgx` appears only as an indirect dependency in its module graph.
- [ ] Add an authenticated, filtered, paginated investigation API/view and enforce OBACloud server/agency authorization.
- [ ] Add tests for episode open/close semantics, complete vs differential feed handling, metadata versions, speed rules, and retention. Add PostgreSQL outage tests only if that deferred feature is approved.
- [ ] If multi-instance Watchdog is planned, write the migration/lease design and update `CLAUDE.md` with a warning before enabling multiple pollers. Include earliest PostgreSQL outage-start reconciliation across replicas.
- [ ] Revisit retention/partitioning after measuring actual production event volume.
- [ ] Keep per-stop prediction accuracy from issue #27 out of this schema until its source and formulas are specified.

## References

- [Watchdog issue #156: durable historical investigation storage](https://github.com/OneBusAway/watchdog/issues/156)
- [Watchdog issue #157: retain unmatched-trip observations](https://github.com/OneBusAway/watchdog/issues/157)
- [Watchdog issue #107: Speed Validation](https://github.com/OneBusAway/watchdog/issues/107)
- [Watchdog vehicle metrics](https://github.com/OneBusAway/watchdog/blob/main/internal/metrics/vehicle_metrics.go)
- [Watchdog metric definitions](https://github.com/OneBusAway/watchdog/blob/main/internal/metrics/metrics.go)
- [Watchdog vehicle state](https://github.com/OneBusAway/watchdog/blob/main/internal/metrics/vehicle_store.go)
- [Watchdog metrics guide](../METRICS.md)
- [Watchdog freshness contract](../FRESHNESS.md)
- [OBACloud vehicle freshness chart](https://github.com/OneBusAway/obacloud/blob/develop/app/services/dashboards/charts/oba_api.rb)
- [OBACloud monitoring alerts](https://github.com/OneBusAway/obacloud-monitoring/blob/main/prometheus/alerts.yml)
- [OBACloud Alertmanager routing](https://github.com/OneBusAway/obacloud-monitoring/blob/main/alertmanager/alertmanager.yml)
- [GTFS-Realtime Position reference](https://gtfs.org/documentation/realtime/reference/#message-position)
- [Maglev SQLite schema](https://github.com/OneBusAway/maglev/blob/main/gtfsdb/schema.sql)
- [Maglev schema runner](https://github.com/OneBusAway/maglev/blob/main/gtfsdb/helpers.go)
- [Maglev sqlc configuration](https://github.com/OneBusAway/maglev/blob/main/gtfsdb/sqlc.yml)
- [OpenStreetMap `maxspeed` tag](https://wiki.openstreetmap.org/wiki/Key:maxspeed)
- [Render scaling](https://render.com/docs/scaling)
- [Render persistent disks](https://render.com/docs/disks)
