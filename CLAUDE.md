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

**Stores are the shared state.** In-memory, `sync.RWMutex`-guarded caches passed by pointer so multiple services see the same data:
- `gtfs.StaticStore` — parsed GTFS static bundles, keyed by server ID.
- `gtfs.RealtimeStore` — most recent parsed GTFS-RT feed.
- `geo.BoundingBoxStore` — per-server geographic bounds (used to flag out-of-bounds vehicles).
- `metrics.VehicleLastSeen` — last-seen timestamps for telemetry/reporting-interval metrics; self-prunes via a `ClearRoutine` goroutine.
- `config.BackoffStore` — per-server exponential backoff state.

**Background goroutines** (started from `main.go`): periodic metrics collection, 24h GTFS bundle refresh, vehicle last-seen cleanup, and (only with `--config-url`) a config refresh loop. All take the root `context.Context` and exit cleanly on cancel.

**The collection loop is the heart of the system.** `StartMetricsCollection` (`internal/app/metrics_collector.go`) ticks every `--fetch-interval` seconds and calls `CollectMetricsForServer` for each server. That function runs probes in a deliberate order; read its doc comment before changing it. Two ordering constraints matter:
- **Backoff is non-blocking and lives in `BackoffStore`** — not a `time.Sleep`. Before each cycle it checks `NextRetryAt`; if a server is still backing off, its entire collection is skipped this tick. A failed ping grows the backoff; a success resets it. This is intentional so backoff never exceeds the fetch interval and goroutines never overlap per server. Do not replace it with a blocking retry.
- **`FetchAndStoreGTFSRTFeed` is a hard gate.** Every check after it (`CountVehiclePositions`, `TrackVehicleTelemetry`, `TrackInvalidVehiclesAndStoppedOutOfBounds`) reads the realtime store, so if the RT fetch fails the function returns early. Earlier checks each log-and-continue on error.

**Metrics are global `promauto` vectors** declared in `internal/metrics/metrics.go` and registered with the default registry at package init. Metric functions look up data from the stores and `.WithLabelValues(...).Set(...)`. Common label keys: `agency_id`, `agency_name`, `server_url`, `vehicle_id`. `/metrics` is served through a caching handler (`middleware.NewCachedPromHandler`, 10s) to limit scrape cost.

**Error reporting goes through `internal/report`** (Sentry wrapper). Collection code logs via the injected `slog.Logger` AND calls `report.ReportErrorWithSentryOptions` with `agency_id`/`agency_name` tags so failures are correlated per server. Match this dual logging when adding collection steps.

**Config** can come from a local file (`--config-file`) or a remote URL (`--config-url`, optionally Basic-Auth'd via `CONFIG_AUTH_USER`/`CONFIG_AUTH_PASS`). Exactly one source is required (`config.ValidateConfigFlags`). A config is a JSON array of `models.ObaServer` objects.

## Testing notes

- Unit tests use `httptest` servers and fixtures under `testdata/`; the metrics package uses `go-vcr` cassettes (`internal/metrics/testdata/vcr/*.yaml`) to replay recorded OBA API responses. Each package has a `test_helpers.go` with constructors like `createTestServer`.
- Because metrics are global vars, tests that assert on them should reset/inspect via the prometheus client model rather than assuming a clean registry.
