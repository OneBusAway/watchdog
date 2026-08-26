# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Watchdog is a Go service that monitors [OneBusAway](https://onebusaway.org/) (OBA) REST API servers and their GTFS feeds, exposing the results as Prometheus metrics. It polls every configured OBA server on a fixed interval, validates static GTFS, GTFS-RT, and OBA API responses, and updates gauges/counters that Prometheus scrapes and Grafana visualizes. See `docs/METRICS.md` for the full metric catalog and interpretation guide.

## Commands

```bash
make test          # go test ./...   (unit tests; same as CI minus coverage)
make vet           # go vet ./...
make fmt           # gofmt -w .  (run before committing)
make compile       # CGO_ENABLED=0 go build -v -o . ./...

go test ./internal/metrics/...                          # single package
go test -run TestCheckBundleExpiration ./internal/metrics/   # single test

# Integration tests are gated behind a build tag and need a real config:
go test -tags=integration ./internal/integration/ -integration-config path/to/integration_config.json
```

CI (`.github/workflows/ci.yml`) runs `go test -v -coverprofile=profile.cov ./...` and uploads to Coveralls. Integration tests are NOT run in CI — they require live OBA/GTFS endpoints.

Running locally requires a `config.json` (git-ignored). Copy `config.json.template`, fill it in, then:
```bash
go run ./cmd/watchdog/ --config-file config.json
```
Or run the full stack (Watchdog :4000, Prometheus :9090, Grafana :3000) with `docker compose up --build`.

## Architecture

**Dependency wiring happens in one place.** `app.New()` (`internal/app/app.go`) constructs every store and service and injects them. `cmd/watchdog/main.go` is the only `main`: it parses flags, loads config, builds the `Application`, kicks off the background goroutines, and starts the HTTP server. When adding a service or store, wire it through `app.New()` rather than constructing it ad hoc — constructor injection is what makes the metric functions testable.

**Service / store separation.** Each domain package (`config`, `gtfs`, `geo`, `metrics`) exposes a `*Service` struct holding injected dependencies (logger, HTTP client, stores). The Service methods are thin wrappers that delegate to lowercase package-private functions (e.g. `MetricsService.CheckBundleExpiration` → `checkBundleExpiration`). The private functions take their dependencies as explicit args, so tests call them directly without building a full Service. Follow this pattern: exported method on the Service for production wiring, private function with explicit params for the logic.

**Stores are the shared state.** In-memory, `sync.RWMutex`-guarded caches passed by pointer so multiple services see the same data. All but the route index are keyed by a *server key* — `models.ServerKey(oba_base_url, agency_id)`, the sanitized base URL and the agency joined by `|`. Agency IDs are only unique within one OBA deployment, so nothing may be keyed on `agency_id` alone:
- `gtfs.StaticStore` — parsed GTFS static bundles. In server-mode one merged bundle pointer is registered under every agency `agency.txt` declares.
- `gtfs.RealtimeStore` — the most recent merged GTFS-RT feed per server key. A server-scoped entry stores its feed under the *server-scoped* key (`models.ServerKey(baseURL, "")`, empty agency), because the vehicle pass runs once for the whole server and reads that single key.
- `geo.BoundingBoxStore` — geographic bounds per server key (used to flag out-of-bounds vehicles). Server-mode publishes the same box under the server-scoped key too, for the same reason.
- `gtfs.RouteAgencyIndex` — `route_id` → `agency_id` (plus `agency_id` → `agency_name`), built during static-bundle parse and keyed by the **raw** `oba_base_url`, not the sanitized one. It is how server-mode attributes an RT vehicle to an agency.
- `metrics.VehicleLastSeen` — last-seen timestamps for telemetry/reporting-interval metrics; self-prunes via a `ClearRoutine` goroutine.
- `metrics.UnmatchedStopTracker` — label values of unmatched-stop gauge series so they can be deleted once unseen; also self-prunes via `ClearRoutine`.
- `config.BackoffStore` — per-server exponential backoff state.

**Background goroutines** (started from `main.go`): periodic metrics collection, 24h GTFS bundle refresh, vehicle last-seen cleanup, unmatched-stop series cleanup, and (only with `--config-url`) a config refresh loop. All take the root `context.Context` and exit cleanly on cancel. `RefreshGTFSBundles` takes a `func() []models.ObaServer` supplier (`app.ConfigService.Config.GetServers`) rather than a slice, and calls it on every tick — using the boot-time list meant that a server added later never had its bundle downloaded, while a server removed later kept being fetched.

**The collection loop is the heart of the system.** `StartMetricsCollection` (`internal/app/metrics_collector.go`) ticks every `--fetch-interval` seconds and dispatches each configured entry through `collectForScope`. Agency-scoped entries go to `CollectMetricsForServer`; server-scoped ones to `collectForServerScope`. Read those doc comments before changing anything. Three constraints matter:
- **Backoff is non-blocking and lives in `BackoffStore`** — not a `time.Sleep`. `collectAgencyChecks` checks `NextRetryAt` before each cycle and returns false if the entry is still backing off, so its collection is skipped this tick; server-mode additionally gates on the agency-less server key at the top of `collectForServerScope`, since that is the key its own ping failures write to. A failed ping grows the backoff; a success resets it. This is intentional so backoff never exceeds the fetch interval and goroutines never overlap per server. Do not replace it with a blocking retry.
- **`FetchAndStoreGTFSRTFeed` is a hard gate in agency-mode only.** All three passes in `collectVehicleMetrics` (`CountVehiclePositions`, `TrackVehicleTelemetry`, `TrackInvalidVehiclesAndStoppedOutOfBounds`) read the realtime store, so `CollectMetricsForServer` returns on a failed RT fetch rather than emit metrics derived from a stale or absent feed. `collectForServerScope` deliberately does *not* return: it logs, reports to Sentry, and runs the pass anyway against whatever the previous tick left under the server-scoped key (nothing at all on the first tick). The fetch there is one step shared by every agency on the server, so a blip is surfaced as a Sentry report instead of dropping the whole server's vehicle pass; the accepted trade-off is that the pass may recompute from a feed one or more ticks old, which is what the report is there to flag. When no agency is live, server-mode skips both the fetch and the pass. Everything before the RT fetch logs-and-continues in both modes — only backoff and a failed ping stop a tick.
- **The vehicle pass runs once per server per tick, never once per agency.** `collectVehicleMetrics` walks the whole GTFS-RT feed and attributes each vehicle to its owning agency through `gtfs.RouteAgencyIndex` (`route_id` → `agency_id`). Calling it per agency re-walks the same merged feed, which permanently inflates the `VehicleReportCount` counter and files every vehicle under every agency's last-seen slot. Its `agencies` argument is the scope dispatch: `nil` means agency-mode (trust `server.AgencyID`, don't consult the index), non-nil means server-mode. This is why the per-agency probes live in `collectAgencyChecks` (backoff, ping, bundle expiration, OBA API metrics, vehicles-for-agency) — both modes call it once per agency, and the vehicle pass is deliberately outside it, in the caller.

**Removing a server has to clean up after itself.** `app.PruneStaleServers` runs after every `--config-url` refresh: it drops the departed server's entries from every store *and* deletes its Prometheus series via `metrics.DeleteSeriesForServer`. Without the second half, `/metrics` keeps exposing the server frozen at its last value, which reads to an alert as healthy rather than absent. Metric vectors declared in `internal/metrics/metrics.go` are wrapped in `tracked(...)` so they register themselves for pruning — keep that wrapper when adding a vector.

Series retirement is deliberately *not* edge-triggered on "a store key was removed this refresh". A config refresh can land mid-tick, so the in-flight collection can re-write a departed server's gauges right after they were deleted; if that tick wrote nothing to a store (a failed ping only sets `oba_api_status` and grows the backoff), a key-driven prune would never notice again and the series would sit on `/metrics` forever. `PruneStaleServers` therefore also retires every base URL in `KnownServerSet.DepartedURLs` — everything ever configured that isn't configured now — so a resurrected series is cleaned up on the next refresh regardless of what resurrected it.

**`app.KnownServerSet`** (`internal/app/newcomers.go`) is the memory of what the configuration used to look like, seeded in `app.New` from the boot config. It holds two spans deliberately: `keys` (`ServerKey()` identities, replaced wholesale each refresh) answers "which entries are new?" for `NewlyAddedServers`, which triggers an immediate bundle download so a newly added server doesn't wait up to 24h; `everConfiguredURLs` (base URLs, accumulated, never dropped) answers "what has ever departed?" for the pruning above. Newcomer-ness must be a property of the *config*, not of the stores: the refresh callback fires on every successful load, not only on change, so asking "does this server have a bundle yet?" makes a server whose download keeps failing a newcomer every single minute.

**Metrics are global `promauto` vectors** declared in `internal/metrics/metrics.go` and registered with the default registry at package init. Metric functions look up data from the stores and `.WithLabelValues(...).Set(...)`. Common label keys: `agency_id`, `agency_name`, `server_name`, `server_url`, `vehicle_id`. `/metrics` is served through a caching handler (`middleware.NewCachedPromHandler`, 10s) to limit scrape cost.

**Error reporting goes through `internal/report`** (Sentry wrapper). Collection code logs via the injected `slog.Logger` AND calls `report.ReportErrorWithSentryOptions` with a `server_name` tag — plus `agency_id`/`agency_name` when the step is agency-scoped — so failures are correlated per server and per agency. Match this dual logging when adding collection steps.

**Config** can come from a local file (`--config-file`) or a remote URL (`--config-url`, optionally Basic-Auth'd via `CONFIG_AUTH_USER`/`CONFIG_AUTH_PASS`). Exactly one source is required (`config.ValidateConfigFlags`). A config is a JSON array of `models.ObaServer` objects.

## Testing notes

- Unit tests use `httptest` servers and fixtures under `testdata/`; the metrics package uses `go-vcr` cassettes (`internal/metrics/testdata/vcr/*.yaml`) to replay recorded OBA API responses. Each package has a `test_helpers.go` with constructors like `createTestServer`.
- Because metrics are global vars, tests that assert on them should reset/inspect via the prometheus client model rather than assuming a clean registry.
