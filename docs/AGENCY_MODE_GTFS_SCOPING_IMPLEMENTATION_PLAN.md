# Agency-Mode GTFS Scoping Implementation Plan

## Audience and Goal

This document is an implementation brief. Read it together with the repository-level `CLAUDE.md` before changing code. Do not rely on assumptions from earlier implementations or redesign unrelated collection behavior.

The goal is to eliminate cross-agency GTFS static and GTFS-realtime contamination in agency mode while preserving server mode's existing operational model.

An agency-mode configuration entry has a non-empty `agency_id`. That configured `agency_id` is the sole target and default agency. Do not add a `default_agency_id` field: it would necessarily equal `agency_id` and would create two names for one concept.

After this work, agency mode must persist and report only static and realtime data that can be associated with the configured agency. Server mode must continue using a consolidated static bundle, one consolidated realtime feed, and one realtime vehicle pass per server.

## Final Decisions

These decisions are requirements, not open design questions:

1. In agency mode, configuration `agency_id` is the only observed agency and the default for valid unqualified single-agency static data.
2. Do not add `default_agency_id`.
3. Resolve realtime vehicle ownership through both `route_id` and `trip_id`.
4. Extend and reuse the existing `RouteAgencyIndex`; do not create a parallel attribution subsystem.
5. In agency mode, persist only the configured agency's resolved static data. Do not persist the complete consolidated static bundle.
6. In agency mode, persist only realtime vehicles resolved to the configured agency.
7. Foreign, unknown, and conflicting agency-mode realtime vehicles must not receive configured-agency labels or state.
8. In server mode, preserve the shared consolidated static pointer registered under discovered agency keys.
9. In server mode, preserve the consolidated realtime entry under the empty-agency server key.
10. In server mode, preserve one realtime fetch and one vehicle pass per server per tick.
11. Server-mode unattributed behavior remains: unresolved vehicles are accounted for by the existing server-scoped unattributed/quality paths.
12. Do not retain complete trips or stop times after static processing. Retain only compact attribution maps and the resolved static data used by metrics.
13. Keep all current Prometheus metric names and label schemas.

## Current Behavior Verified in the Code

Read these files first:

- `internal/gtfs/gtfs_bundles.go`
- `internal/gtfs/gtfs_service.go`
- `internal/gtfs/static_store.go`
- `internal/gtfs/realtime_store.go`
- `internal/gtfs/route_agency_index.go`
- `internal/models/gtfs_models.go`
- `internal/models/oba_server.go`
- `internal/config/scoping.go`
- `internal/metrics/vehicle_metrics.go`
- `internal/metrics/metrics_service.go`
- `internal/metrics/bundle_expiration.go`
- `internal/metrics/oba_rest_api_metrics.go`
- `internal/app/metrics_collector.go`

### Current Agency Mode

`storeStaticForServer` merges every configured static bundle into one `*models.StaticData`. For an agency-scoped entry, it creates one storage key using the configured agency and stores the entire merged bundle under that key:

```text
server-url|configured-agency -> static data for all agencies in all configured feeds
```

If two agency-mode configuration entries use the same shared feeds, each entry downloads and parses its own copy; they do not share one global pointer. Each entry can therefore retain a separate complete merged bundle.

GTFS-RT parsing similarly merges every configured realtime feed and stores the result under the configured agency key. `attributeVehicle` currently bypasses the route index when its `agencies` argument is nil and treats every vehicle as the configured agency.

### Current Server Mode

In server mode, `storeStaticForServer` stores the same consolidated static pointer under every discovered agency key:

```text
server-url|agency-a --+
server-url|agency-b --+--> same consolidated *models.StaticData
server-url|agency-c --+
```

This pointer sharing is intentional and memory-efficient. Preserve it in this change.

Server-mode GTFS-RT data is stored once under:

```text
server-url|
```

The vehicle pass runs once and attributes vehicles through `RouteAgencyIndex`. Preserve this structure. Never move the realtime vehicle pass into the server-mode per-agency loop.

### Relationships Currently Lost

The external `go-gtfs` parser provides this graph:

```text
Route.Agency
ScheduledTrip.Route
ScheduledTrip.Service
ScheduledTrip.StopTimes
ScheduledStopTime.Stop
Stop.Parent
```

`models.StaticData` currently retains only agencies, routes, services, and stops. Trips and stop times are discarded before Watchdog derives agency-specific service and stop ownership.

The implementation must traverse the complete parser graph while it is available, produce compact results, and then allow the complete parser graph to be garbage collected.

## Required Result by Mode

### Agency Mode Static State

For configured agency A, persist:

- Agency A's agency record or a normalized configured-agency record.
- Routes resolved to A.
- Services referenced by trips on A's routes.
- Stops referenced by stop times on A's trips.
- Parent stations/locations required by those stops.
- A compact `route_id -> agency_id` map for attribution.
- A compact `trip_id -> agency_id` map for attribution.
- A bounding box calculated only from A's persisted stops.
- The normal static fetch timestamp.

Do not persist:

- Agency B's routes.
- Services used only by B's trips.
- Stops used only by B's trips.
- The complete merged static bundle.
- Trips or stop times as full parser structs after projection construction.

### Agency Mode Realtime State

For configured agency A, retain a realtime vehicle only when the shared attribution resolver returns A. Preserve the vehicle's original `FeedID`.

Drop from A's realtime snapshot:

- Vehicles resolved to B.
- Vehicles unresolved by both route and trip.
- Vehicles whose route and trip resolve to different agencies.
- Vehicles lacking both usable route and trip IDs.

An empty filtered result is valid and must be stored as an empty `RealtimeData`. It ensures gauges can fall to zero.

### Server Mode State

Keep the current consolidated static storage and realtime storage behavior. Add trip-based attribution to the existing index and resolver, but do not split the server-mode static bundle into per-agency copies in this change.

## Terminology

Use "agency-scoped static data" or "agency-scoped static snapshot" in code comments and documentation. Avoid the abstract word "projection" unless immediately defined.

An agency-scoped static snapshot is the subset of static data related to one agency, derived through route, trip, service, stop-time, and stop relationships.

## Attribution Index Refactor

### Extend the Existing Type

Refactor `internal/gtfs/route_agency_index.go`. Keep and extend `RouteAgencyIndex` instead of introducing another store.

Its per-scope state should conceptually become:

```go
type serverIndex struct {
    routeIDs    map[string]string // route_id -> agency_id
    tripIDs     map[string]string // trip_id -> agency_id
    agencyNames map[string]string // agency_id -> agency_name
}
```

Keeping the existing type name is the smallest change even though it will also contain trip attribution. Update its comments to make the broader behavior explicit.

### Scope the Index by Server Key

The current index is keyed only by raw `oba_base_url`. That allows separate agency-mode entries sharing a base URL to replace each other's maps. Refactor it to key entries by `models.ServerKey` identity:

- Agency mode index key: `server.ServerKey()`, including configured `agency_id`.
- Server mode index key: `server.ServerKey()`, whose agency component is empty.

Use the same sanitized composite identity as other stores. Do not continue the raw URL versus sanitized store split.

Update these call sites and related tests:

- Index publication from `storeStaticForServer`.
- Index lookup from realtime filtering.
- Index lookup from `attributeVehicle` or its replacement.
- Agency-name lookup in `internal/config/scoping.go`.
- Index pruning in `internal/app/prune.go`.
- `RangeServerKeys`, `Clear`, and `PruneServers` semantics and names if needed.

Do not key the agency-mode index under the empty-agency server key. Multiple agency entries on one base URL must remain independent.

### Publish Route, Trip, and Name Maps Together

Prefer one replacement operation that installs route mappings, trip mappings, and agency names together under one lock. The current `Set` followed by several `SetAgencyName` calls permits a transient state with routes but no names.

A suitable API shape is:

```go
func (idx *RouteAgencyIndex) Replace(
    serverKey string,
    routes map[string]string,
    trips map[string]string,
    agencyNames map[string]string,
)
```

The exact name is flexible. Avoid retaining obsolete compatibility methods if all repository callers can be updated directly.

### Identifier Collisions

Do not use silent last-write-wins attribution when the same identifier resolves to different agencies across configured static feeds.

While building each map:

- Repeated ID with the same agency remains resolvable.
- Repeated ID with a different agency becomes ambiguous.
- Remove ambiguous IDs from the final resolvable map, or track ambiguity explicitly.
- Report a warning through the existing logger/Sentry conventions with server and identifier context.

This applies to both route IDs and trip IDs.

## Shared Vehicle Resolver

Add one resolver to `RouteAgencyIndex`, conceptually:

```go
func (idx *RouteAgencyIndex) ResolveVehicleAgency(
    serverKey string,
    routeID string,
    tripID string,
) (agencyID string, ok bool)
```

Resolution rules:

| Route result | Trip result | Resolution |
|---|---|---|
| A | A | A |
| A | unknown or absent | A |
| unknown or absent | A | A |
| A | B | unresolved conflict |
| unknown or absent | unknown or absent | unresolved |

Do not always prefer route over trip or trip over route when both resolve and disagree. A disagreement is inconsistent source data and must not be assigned to either agency.

Use this resolver in both modes:

- Agency-mode fetch filtering accepts only `ok && agencyID == server.AgencyID`.
- Server-mode metric attribution resolves the agency and then checks the existing eligible/live agency map.

The realtime parser exposes IDs as:

```go
vehicle.Trip.ID.RouteID
vehicle.Trip.ID.ID
```

Handle a nil `vehicle.Trip` as unresolved.

## Static Attribution Construction

Build compact attribution maps while the original `[]*remoteGtfs.Static` is available.

### Resolve Route Ownership

For each source route:

1. Ignore an empty route ID for indexing.
2. Resolve the route's agency from `route.Agency`.
3. In server mode, a non-empty parsed agency ID is required.
4. In agency mode, a non-empty parsed agency ID remains authoritative, including when it differs from configured `agency_id`.
5. In agency mode, if ownership is unqualified in a valid single-agency static feed, use configured `server.AgencyID` as the default.
6. Never overwrite an explicit foreign agency ID with configured `agency_id`.

`go-gtfs` already associates a route with the sole agency when `routes.agency_id` is omitted and `agency.txt` has exactly one row. Its resulting `Route.Agency` may still have a blank ID because `agency_id` itself is optional in a single-agency dataset. Agency mode may normalize that blank sole-agency ownership to configured `agency_id`.

When multiple agencies exist and a route omits `agency_id`, `go-gtfs` skips the route as ambiguous. Do not try to guess ownership afterward.

### Resolve Trip Ownership

For each `bundle.Trips` entry:

1. Require a non-empty trip ID for indexing.
2. Resolve its agency through `trip.Route` using the same effective route-ownership rule.
3. Record `trip.ID -> agency_id` when ownership is resolved.
4. Apply collision handling described above.

Do not infer agency ownership from a service or stop. Services and stops are many-to-many with agencies and are not authoritative ownership roots.

## Agency-Scoped Static Snapshot Builder

Add a private helper in `internal/gtfs`, preferably in a focused new file such as `internal/gtfs/agency_static.go` if `gtfs_bundles.go` becomes harder to follow.

The helper should accept the configured server and parsed source bundles and return only the configured agency's compact snapshot plus its attribution maps. Keep logic package-private and test it directly, following the repository service/private-function pattern.

Conceptual output:

```go
type agencyStaticResult struct {
    data        *models.StaticData
    routeIDs    map[string]string
    tripIDs     map[string]string
    agencyNames map[string]string
}
```

The exact private type is flexible.

### Select Routes

Include every source route resolved to configured `server.AgencyID`, even if no parsed trip references that route. Static route count is based on declared agency routes, not only currently scheduled routes.

Exclude explicitly foreign and unresolved routes from the persisted agency snapshot.

Deduplicate route IDs within the agency snapshot. Preserve first occurrence for the stored route row, report materially conflicting duplicates consistently with existing collision reporting, and keep ambiguous cross-agency IDs out of attribution maps.

### Select Services

Walk source trips resolved to configured `server.AgencyID`. Include each non-nil `trip.Service` once per source service object.

Do not deduplicate solely by `service_id`: independently configured feeds can reuse a service ID with different date ranges. Pointer identity during construction is sufficient to avoid adding one service once per trip while preserving distinct source service rows.

Copy retained service values into the compact snapshot. Copy `AddedDates` and `RemovedDates` slices if necessary to ensure the retained snapshot owns only its selected calendar data.

### Select Stops

For each trip resolved to configured `server.AgencyID`, walk `trip.StopTimes` and select each non-nil `stopTime.Stop`.

Include the complete `Stop.Parent` chain for every selected stop because unmatched-stop clustering calls `Stop.Root()` and parent stations are part of the required scoped representation.

Use a visited set while traversing parents to prevent malformed cycles from looping forever.

A stop used by both A and B legitimately belongs to both agencies. In agency mode A's compact snapshot includes it because an A trip references it. A stop used only by B must not be retained for A.

Deduplicate selected stops by `stop_id`, preserving existing first-occurrence behavior and collision warnings where applicable.

### Rewire Pointers for Real Memory Release

Do not merely shallow-copy selected stops and routes.

A copied `Stop.Parent` pointer can point into the original source stop backing array, and a copied `Route.Agency` can point into the original source agency backing array. Those pointers can keep source arrays reachable and defeat the goal of releasing the consolidated parsed bundle.

Build final owned slices, then rewire:

- Every retained route's `Agency` points to the normalized agency object inside the compact snapshot.
- Every retained stop's `Parent` points to the corresponding retained parent object inside the final compact stop slice.
- No retained pointer points into an unselected source array.

Allocate the final slices at their complete length before taking element addresses so later append operations cannot invalidate internal pointers.

Do not take addresses of Go range-variable copies.

### Agency Record

The compact snapshot should contain one agency identity representing configured `server.AgencyID`.

- If static `agency.txt` contains an exact matching non-empty ID, retain its metadata and use its non-empty name where appropriate.
- If a valid sole agency has a blank ID, copy its metadata but normalize its ID to configured `server.AgencyID`.
- Otherwise use configured `AgencyID` and `AgencyName` without borrowing another explicitly identified agency's metadata.

## Refactor `storeStaticForServer`

Split the two modes early enough that agency mode does not first create and retain a merged `StaticData` unnecessarily.

### Agency Mode Path

1. Build the compact configured-agency snapshot directly from parsed source bundles.
2. Build route and trip attribution maps while source relationships are available.
3. Store only that snapshot under `server.ServerKey()`.
4. Store its fetch time under the same key.
5. Compute and store its bounding box from only its retained stops.
6. Publish its route/trip/name maps under its agency-scoped index key.
7. Invoke the static observer with the compact snapshot.
8. Return without calling the server-mode consolidated merge path.

An empty but successfully constructed compact snapshot should replace an old one. Do not preserve stale merged data when a new bundle contains no data attributable to the configured agency.

### Server Mode Path

Preserve the existing consolidated `mergeStaticAndDiscoverAgencies` result and shared pointer storage.

Add trip map construction before parsed trips become unreachable. Publish route, trip, and agency-name maps under the empty-agency server key.

Do not build one full static copy per discovered agency.

Do not change the server-mode once-per-server realtime architecture.

## Bounding Box Behavior

### Agency Mode

Compute the bounding box from the compact snapshot's selected stops, including selected parents. Do not compute it by assigning every source-feed stop to every agency declared by that feed.

Do not fall back to the server-wide union in agency mode. A union can contain another agency's geography and would continue the contamination in stopped/out-of-bounds metrics.

If a successful refresh produces no valid agency-scoped bounding box, remove any old value for `server.ServerKey()` so stale broad bounds do not survive. Add a thread-safe `Delete(serverKey string)` method to `BoundingBoxStore` if needed.

Use the existing logger plus Sentry reporting conventions for a missing/invalid scoped box.

### Server Mode

Leave current bounding-box behavior unchanged in this task, including the server-wide union used by the unattributed catch-all. Changing server-mode feed-declaration-based boxes is outside this focused fix.

## Agency-Mode Realtime Filtering

Refactor `fetchAndStoreGTFSRTFeed` and `GtfsService.FetchAndStoreGTFSRTFeed` so the function can use `RouteAgencyIndex`.

After `parseGTFSRTFeeds` succeeds and before writing to `RealtimeStore`:

1. If `server.IsServerScoped()`, store the merged result unchanged.
2. Otherwise, resolve every vehicle using route ID and trip ID through the agency-scoped index key.
3. Retain only vehicles resolved exactly to configured `server.AgencyID`.
4. Preserve each retained `RealtimeVehicle.FeedID`.
5. Store the filtered result under `server.ServerKey()`, including when it is empty.

If no agency-scoped attribution snapshot exists, return an error and do not write an unfiltered feed. Agency mode's application path already treats realtime fetch/store errors as a hard gate. Never restore the old "trust every vehicle" behavior as a fallback.

Filtering before storage is intentional. It guarantees all three agency-mode metric passes consume the same scoped snapshot:

- `CountVehiclePositions`
- `TrackVehicleTelemetry`
- `TrackInvalidVehiclesAndStoppedOutOfBounds`

It also prevents foreign vehicles from entering `VehicleLastSeen`.

## Server-Mode Realtime Attribution Reuse

Update the existing `attributeVehicle` implementation to call the shared route-or-trip resolver.

Server mode must still:

- Build the eligible agency map once.
- Read realtime data from the empty-agency server key.
- Walk the consolidated feed once.
- Match the resolved agency ID against live agencies.
- Count unresolved or conflicting vehicles in the existing unattributed behavior.
- Use agency bounding boxes for attributed vehicles and the union box for the server catch-all.

Agency mode may continue passing nil agencies to metric functions because its realtime store is now guaranteed to contain only prefiltered vehicles. Rewrite comments that currently say the operator's configured agency means every upstream vehicle is trusted. The correct statement is that agency-mode metric passes trust the already filtered store snapshot.

## Downstream Static Consumers

These consumers should become agency-correct without mode-specific filtering once agency mode stores compact scoped data:

- `MetricsService.StaticBundleObserver`: counts only retained routes and stops.
- `checkBundleExpiration`: sees only services referenced by configured-agency trips.
- `getStopLocationsByIDs`: scans only configured-agency stops.
- `fetchObaAPIMetrics`: cannot resolve a configured agency's unmatched stop from a foreign stop.
- `reportUnmatchedStopClusters`: receives only scoped stops with rewired parent relationships.
- stopped/out-of-bounds validation: reads only the configured agency's box.

Do not add repeated filtering to these consumers. Correct the stored source of truth once.

## Store and Pruning Requirements

Update index pruning to match its new composite key semantics. A config entry owns index state under the same rules as its other stores:

- Agency mode owns exactly `server.ServerKey()`.
- Server mode owns the empty-agency server key used for the consolidated attribution index.

Do not accidentally prune server-mode attribution because its static bundles are registered under discovered agency keys while its attribution index is registered under the empty-agency key.

Preserve existing `StaticStore`, `RealtimeStore`, `VehicleLastSeen`, and unmatched-stop pruning behavior unless a direct signature update is required.

## Tests to Add or Update

Write tests before or alongside each behavior change. Prefer small synthetic parser graphs for relationship tests. Construct pointers carefully; do not take pointers to range variables.

### Synthetic Static Graph

Build a fixture with:

- Agency A and agency B.
- A route owned by A.
- An unused route owned by A.
- A route owned by B.
- Distinct service ranges for A and B.
- A trip on A's route and service.
- A trip on B's route and service.
- An A-only stop.
- A B-only stop geographically far from A.
- A shared stop used by both agencies.
- A child stop with a parent station.
- Route and trip IDs that collide across agencies for ambiguity tests.

### Agency Static Snapshot Tests

Add focused tests in `internal/gtfs`, likely `agency_static_test.go` or `gtfs_bundles_test.go`:

- A snapshot contains only A routes, including A's unused route.
- B routes are absent.
- A snapshot contains only services referenced by A trips.
- A snapshot contains A-only and shared stops, not B-only stops.
- Parent stations are included.
- `Stop.Root()` follows pointers inside the compact snapshot.
- Retained `Route.Agency` points to the compact normalized agency object.
- No retained route or stop pointer points into source arrays.
- A matching declared agency retains metadata.
- A sole blank agency ID is normalized to configured `agency_id`.
- An explicit different non-empty agency ID is never normalized to configured `agency_id`.
- Ambiguous multi-agency ownership is not guessed.

For pointer ownership, mutate or discard source slices after construction and verify the compact graph remains internally correct. Do not depend on forcing garbage collection as a test assertion.

### Static Storage Tests

Update tests that currently pin the contaminated behavior:

- Agency mode stores only one compact configured-agency snapshot.
- Agency mode does not retain the consolidated bundle.
- The observer receives scoped route and stop counts.
- Fetch time remains recorded under configured `server.ServerKey()`.
- A configured agency mismatch does not store another agency's routes/stops/services.
- Server mode still stores the same consolidated pointer under every discovered agency key.

The current `TestStoreGTFSBundleRecordsFetchTime` configures `agency-1` while the fixture declares agency `40` and expects the complete fixture under `agency-1`. Change the positive test to configure `40`. Add a separate mismatch test asserting that foreign static data is excluded.

### Service Expiration Tests

Add tests in `internal/metrics/bundle_expiration_test.go`:

- A's expiration uses only A-referenced services.
- B-only service dates do not change A's earliest or latest expiration.
- A compact snapshot with no referenced services returns the existing no-services error.

### Stop and Bounding-Box Tests

Add or update tests in `internal/gtfs` and `internal/metrics`:

- A lookup cannot resolve a B-only stop under A's server key.
- A shared stop resolves under A.
- A's bounding box includes A-only/shared/required-parent locations.
- A's bounding box excludes B-only geography.
- Agency mode never falls back to union bounds.
- A successful refresh with no valid scoped coordinates deletes an old agency box.
- Server-mode union behavior remains unchanged.

Replace the current expectation that every agency declared by a multi-agency source feed necessarily receives that feed's entire stop bounds, but only for the agency-mode path. Preserve tests covering current server-mode behavior.

### Attribution Index Tests

Update `internal/gtfs/route_agency_index_test.go`:

- Route-only resolution works.
- Trip-only resolution works.
- Matching route and trip resolution works.
- Conflicting route and trip resolution fails.
- Neither ID resolving fails.
- Same ID repeated for the same agency remains resolvable.
- Same route ID across agencies becomes ambiguous.
- Same trip ID across agencies becomes ambiguous.
- Agency-mode indexes sharing a base URL remain isolated by server key.
- Server-mode empty-agency index remains independently addressable.
- Replace publishes agency names with route and trip maps.
- Pruning uses composite server-key ownership correctly.

### Realtime Filtering Tests

Add tests around `fetchAndStoreGTFSRTFeed` with vehicles containing:

- A known A route and known A trip.
- A known A route without trip ID.
- A known A trip without route ID.
- A B route and B trip.
- An unknown route and known A trip.
- A known A route and unknown trip.
- An A route and conflicting B trip.
- Unknown route and unknown trip.
- No trip descriptor.
- Duplicate vehicle IDs from separate feeds.

Assert in agency mode:

- Both route-only and trip-only A vehicles are retained.
- Unknown route plus known A trip is retained.
- Known A route plus unknown trip is retained.
- B, conflicting, and fully unknown vehicles are removed.
- Feed IDs are unchanged.
- An empty filtered feed is stored as empty.
- Missing attribution state returns an error and never stores unfiltered data.

Assert in server mode:

- The merged realtime feed is stored unchanged.
- Route-only and trip-only vehicles are attributed during the single vehicle pass.
- Conflicts are counted as unattributed.

### Metric Regression Tests

Replace `TestTrackVehicleTelemetryAgencyModeIgnoresRouteIndex` because its expected behavior is the bug being fixed. The replacement should exercise the normal fetch/filter/store path and then the metric pass.

Assert:

- Agency-mode position count includes only retained A vehicles.
- `VehicleLastSeen` contains only A vehicles.
- `VehicleReportCount` does not increment for B, conflicting, or unknown vehicles under A labels.
- Invalid coordinates from a B vehicle are not counted under A.
- A B vehicle is not tested against A's bounding box.
- Empty filtered snapshots zero aggregate agency gauges through existing emitters.

Preserve server-mode regression coverage in:

- `internal/app/server_scope_rt_test.go`
- `internal/app/server_scope_vehicle_pass_test.go`
- `internal/metrics/vehicle_metrics_server_mode_test.go`

These must continue proving one fetch, one pass, no counter multiplication, per-agency last-seen state, and server-scoped unattributed handling.

## Documentation and Comments

Update stale descriptions after implementation:

- `CLAUDE.md`
- `README.md`
- `docs/METRICS.md`
- `cmd/watchdog/main.go`
- `internal/models/gtfs_models.go`
- `internal/models/oba_server.go`
- `internal/config/scoping.go`
- `internal/gtfs/gtfs_bundles.go`
- `internal/gtfs/gtfs_service.go`
- `internal/gtfs/route_agency_index.go`
- `internal/metrics/vehicle_metrics.go`
- `internal/metrics/metrics_service.go`
- relevant tests whose names/comments pin old behavior

Document these user-visible semantics:

- Agency mode stores only static data associated with configured `agency_id`.
- Configured `agency_id` is the sole default for valid unqualified single-agency static data.
- Agency mode filters realtime vehicles through route or trip attribution.
- Unknown, foreign, and conflicting vehicles are not labeled as configured agency.
- Server mode retains consolidated static and realtime processing.
- Server mode resolves vehicles by route or trip and retains its unattributed bucket.

## Non-Goals

Do not expand this work into unrelated redesigns:

- Do not deduplicate OBA `/metrics.json` requests.
- Do not change server-mode stale realtime replay semantics.
- Do not redesign realtime feed identity or labels.
- Do not add `default_agency_id`.
- Do not split server-mode static data into per-agency copies.
- Do not move the server-mode vehicle pass into a per-agency loop.
- Do not change metric names or label sets.
- Do not add persisted full trip or stop-time slices.
- Do not redesign config overlap validation beyond making index keys safe for existing overlapping base URLs.
- Do not change static refresh scheduling or backoff behavior.
- Do not alter general Prometheus series retirement unless directly required by a scoped store key change.

## Suggested Implementation Order

1. Add synthetic static graph helpers and failing tests for agency-scoped relationships.
2. Refactor `RouteAgencyIndex` to composite keys, route maps, trip maps, names, collision handling, and shared resolution.
3. Update server-mode index callers and prove existing server attribution still passes.
4. Implement the compact agency static snapshot builder with owned/rewired pointers.
5. Split agency and server paths in `storeStaticForServer`.
6. Compute agency-mode bounds from compact selected stops and delete stale boxes when unavailable.
7. Update static observer, expiration, unmatched-stop, and bounding-box tests.
8. Filter agency-mode realtime data before publishing it to `RealtimeStore`.
9. Update server-mode `attributeVehicle` to use route-or-trip resolution.
10. Add end-to-end agency-mode metric regressions.
11. Update comments and documentation.
12. Run formatting, unit tests, vetting, and compilation.

Keep each step compiling where practical. Do not mix broad cleanup into functional commits or patches.

## Verification Commands

Run focused tests while iterating:

```bash
go test ./internal/gtfs/...
go test ./internal/metrics/...
go test ./internal/config/...
go test ./internal/app/...
```

Run the complete repository checks before considering the work finished:

```bash
make fmt
make test
make vet
make compile
```

Integration tests require a real configuration and are not part of normal CI:

```bash
go test -tags=integration ./internal/integration/ -integration-config path/to/integration_config.json
```

Do not claim integration coverage if no live configuration was supplied.

## Completion Checklist

- [ ] No `default_agency_id` was introduced.
- [ ] Configured `agency_id` is the agency-mode target/default.
- [ ] Agency-mode static state contains only resolved configured-agency data.
- [ ] Complete consolidated static bundles are not retained in agency mode.
- [ ] Retained stop and route pointers do not keep unselected source arrays alive.
- [ ] Route and trip attribution maps are both built.
- [ ] Route-only realtime attribution works.
- [ ] Trip-only realtime attribution works.
- [ ] Conflicting route/trip attribution fails safely.
- [ ] Agency mode stores only realtime vehicles resolved to configured agency.
- [ ] Foreign and unknown vehicles create no configured-agency telemetry or last-seen state.
- [ ] Agency-mode expiration uses only associated services.
- [ ] Agency-mode unmatched stops use only associated stops.
- [ ] Agency-mode bounding boxes use only associated stops and never union fallback.
- [ ] Server mode still pointer-shares one consolidated static bundle.
- [ ] Server mode still stores one consolidated realtime feed under its empty-agency key.
- [ ] Server mode still runs one realtime vehicle pass per tick.
- [ ] Server mode can attribute by route or trip.
- [ ] Server-mode unresolved/conflicting vehicles retain unattributed behavior.
- [ ] All modified comments and docs describe the new semantics.
- [ ] `make fmt`, `make test`, `make vet`, and `make compile` pass.
