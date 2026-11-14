# Watchdog

[![Coverage Status](https://coveralls.io/repos/github/OneBusAway/watchdog/badge.svg?branch=main)](https://coveralls.io/github/OneBusAway/watchdog?branch=main)

**Watchdog** is a Go-based service that monitors [OneBusAway (OBA)](https://onebusaway.org/) REST API servers.
It exposes a comprehensive set of **Prometheus metrics** for monitoring:

- GTFS Static and GTFS-RT data integrity
- Vehicle telemetry
- Agency and stop coverage
- Overall operational health
  See the full list of metrics and interpretation guide [here](./docs/METRICS.md), and the client freshness contract in [docs/FRESHNESS.md](./docs/FRESHNESS.md).

## Requirements

- **Go 1.27.2+**

## Setup

### Configuration

Watchdog requires a configuration file (`config.json`) before running. Even placeholder data is necessary to start the service.

#### Example `config.json`

```json
[
  {
    "server_name": "Test Server 1",
    "oba_base_url": "https://test1.example.com",
    "oba_api_key": "test-key-1",
    "gtfs_static_feeds": ["https://gtfs1.example.com"],
    "gtfs_rt_feeds": [{
      "trip_update_url": "https://trip1.example.com",
      "vehicle_position_url": "https://vehicle1.example.com",
      "gtfs_rt_api_key": "api-key-1",
      "gtfs_rt_api_value": "api-value-1"
    }]
  }
]
```

The top-level `agency_id` decides what each entry observes: set it to track that agency. OBA API probes, static data, and realtime feed data are scoped to that agency; leave it out to track the server and every agency it serves. See [Two observation modes](#two-observation-modes-agency-vs-server) below for both config shapes.

#### Feed contract and migration

Each entry requires exactly one URL in `gtfs_static_feeds` and one source in `gtfs_rt_feeds`. The array field names remain unchanged, so existing single-feed v2 and legacy v1 configurations continue to work. A realtime source may include vehicle-position and trip-update URLs; Watchdog currently monitors vehicle positions.

Both feeds must already use the final IDs served by the OBA backend. A merged static feed may contain several agencies. Watchdog preserves IDs and does not merge sources or combine stops.

Vehicle-position vehicle IDs must be unique across the entire merged realtime feed, including across agencies. If two source agencies each use vehicle ID `101` for different vehicles, the upstream merger must give those vehicles distinct final IDs. A trip-update entity may reference the same vehicle ID as its vehicle-position entity; that reference is not a duplicate vehicle position.

To upgrade an existing multi-feed entry:

1. Configure an external merger to produce the static and realtime artifacts consumed by your OBA backend.
2. Replace `gtfs_static_feeds` with the single merged static URL and `gtfs_rt_feeds` with the single merged realtime source, including its authentication fields.
3. Update any configured `agency_id` to its final ID in the merged feed.

Multiple sources are rejected with a migration error. Watchdog does not keep the first source automatically. Invalid entries follow the existing config-loading policy and are excluded while valid entries continue to load. Check the dropped-server diagnostics and `watchdog_configured_servers` after upgrading.

Duplicate primary IDs in the static tables Watchdog consumes reject the new static snapshot, retain the last good snapshot, and produce a Sentry event and `gtfs_static_feed_failure_info{reason="duplicate_id"}`. Realtime duplicate entity IDs or vehicle-position IDs produce a Sentry event and `gtfs_rt_feed_duplicate_id=1`. Every entity carrying a conflicting ID is excluded, and the remaining entities are published as a successful fetch with `gtfs_rt_feed_fetch_success=1`. No duplicate winner is selected. Transport or decoding failures still preserve the cached realtime snapshot without processing it as a new observation.

#### Ways to Provide the Config File

#### 1. Local Configuration (recommended for development)

- Copy or rename `config.json.template` → `config.json`
- Fill in your server values
- Run with:

```bash
go run ./cmd/watchdog/ --config-file path/to/config.json
```

Note:

- ⚠️The file **must** be named `config.json`
- `config.json` is Git-ignored (to protect secrets)

#### 2. Remote Configuration (recommended for production)

- Prepare `config.json` as above
- Host it publicly (or on a private server)
- Run with:

```bash
go run ./cmd/watchdog/ --config-url http://example.com/config.json
```

If authentication is required, set:

```bash
export CONFIG_AUTH_USER="username"
export CONFIG_AUTH_PASS="password"
```

### Two observation modes: agency vs. server

There are two ways Watchdog can observe an OBA deployment. The choice is made **per entry in `config.json`** by whether you set `agency_id` — every other field (`server_name`, `oba_base_url`, `oba_api_key`, `gtfs_static_feeds`, `gtfs_rt_feeds`) is identical in both modes. Setting `agency_id` selects agency observation mode; `agency_name` must also be provided.

- **Agency mode** — `agency_id` is set. Watchdog monitors only that one agency.
- **Server mode (recommended)** — `agency_id` is unset. Watchdog monitors the whole server and every agency it currently serves.

**Observe a single agency** — set `agency_id` (with its `agency_name`) on the entry. Watchdog uses that agency for agency-specific OBA API probes and retains only static and realtime data attributed to it:

```json
{
  "server_name": "Agency Example",
  "agency_name": "Test Agency",
  "agency_id": "agency-1",
  "oba_base_url": "https://test1.example.com",
  "oba_api_key": "test-key-1",
  "gtfs_static_feeds": ["https://gtfs1.example.com"],
  "gtfs_rt_feeds": [{
    "trip_update_url": "https://trip1.example.com",
    "vehicle_position_url": "https://vehicle1.example.com",
    "gtfs_rt_api_key": "api-key-1",
    "gtfs_rt_api_value": "api-value-1"
  }]
}
```

**Observe the whole server** — omit `agency_id` from the entry. Watchdog tracks the server and every agency it currently serves:

```json
{
  "server_name": "Server Example",
  "oba_base_url": "https://test2.example.com",
  "oba_api_key": "test-key-2",
  "gtfs_static_feeds": ["https://gtfs2.example.com"],
  "gtfs_rt_feeds": [{
    "trip_update_url": "https://trip2.example.com",
    "vehicle_position_url": "https://vehicle2.example.com",
    "gtfs_rt_api_key": "api-key-2",
    "gtfs_rt_api_value": "api-value-2"
  }]
}
```

| | Agency mode | Server mode |
|---|---|---|
| `agency_id` | set (paired with required `agency_name`) | unset |
| Scope | one agency | whole OBA deployment |
| Agency set comes from | the config entry | each static feed's `agency.txt`, filtered by `/api/where/metrics.json` agency coverage |
| Typical source | hand-written config | OBACloud service discovery (one entry per OBA API server) |

**Agency mode** — `agency_id` is set. Watchdog monitors only that one agency; today's per-agency pipeline runs once per tick for that agency. `agency_name` is required (paired with `agency_id`).

**Server mode** — `agency_id` is absent. Watchdog probes `/api/where/metrics.json` every tick to learn which agencies are present in OBA's loaded static coverage, cross-references those agency IDs against the static feeds' `agency.txt` declarations, and runs the per-agency pipeline for every agency that has BOTH a static bundle AND is reported by OBA.

Multi-agency static feeds are accepted in server mode: one consolidated bundle pointer-shared across the serverKeys of every agency declared in its `agency.txt`. Agency mode instead stores an owned snapshot containing only the configured agency's routes, referenced services, stops, and parent stations. Feed-to-agency mapping metrics describe the agencies declared in the upstream artifact's `agency.txt`.

#### Static coverage signals

These metrics expose static agency coverage and source-feed mapping health:

- `gtfs_static_agency_reported_by_oba{agency_id, agency_name, server_name, server_url}` — 1 if the agency has a static bundle AND is in `/api/where/metrics.json` `entry.AgencyIDs` in the current scrape; 0 otherwise.
- `gtfs_static_feed_agency_mapping_info{feed_url, agency_id, agency_name, server_name, server_url}` — always 1, with one series for each proven source-feed relationship. An explicit multi-agency feed produces one series per declared agency in server mode. Agency mode emits only its configured agency; a sole agency with omitted `agency_id` is associated with that configured agency as allowed by GTFS.
- `gtfs_static_feed_agency_mapping_failure{feed_url, server_name, server_url, reason}` — always 1 when a parsed feed cannot be fully mapped. `reason` is one of `missing_agency`, `ambiguous_agency`, or `unknown_agency`.

#### Vehicle attribution in server-mode

In agency mode the RT fetch resolves each vehicle by route or trip and stores only vehicles attributed to the configured `agency_id`. Foreign or conflicting vehicles are dropped before the metric pass. Vehicles that cannot be resolved are kept when the static feed names only the configured agency, so quality checks can still report them. Until the first static snapshot loads, the realtime feed is stored unfiltered.

In server mode the single upstream-merged realtime feed can carry vehicles from several agencies. Watchdog attributes each vehicle using the route and trip IDs in the static feed, then checks that OBA reports the agency as live. Unknown IDs, conflicting route/trip ownership, and agencies absent from OBA coverage remain unattributed. Duplicate static primary IDs reject the new snapshot before publication.

### Backward Compatibility (v1 → v2)

This is a **deliberate breaking change** to the existing v2 format. Existing v2 entries without `server_name` become invalid — operators must add the field. The legacy v1 array-of-flat-objects format continues to work: `id` (int) is still ignored, `name` is repurposed as `server_name`, and a v1 entry with `agency_id` populates `agency_name` from `name` so the entry remains agency-scoped.

Watchdog used to accept a flat, single-server config schema. Legacy (v1) configs are still supported: they're **silently converted** to the current array-based schema (v2) at load time, so upgrading doesn't require changing your config or interrupt monitoring.

When a v1 entry is loaded, Watchdog maps it like this:

- `name` → `server_name`; when `agency_id` is present, it also populates `agency_name`
- `gtfs_url` → a single-entry `gtfs_static_feeds`
- `vehicle_position_url` / `trip_update_url` → the matching `gtfs_rt_feeds` entries
- `gtfs_rt_api_key` / `gtfs_rt_api_value` → the per-feed auth fields
- `id` → ignored

One caveat: v1 and v2 fields can't be mixed in the same entry. If an entry contains both schemas (e.g. a legacy `gtfs_url` alongside `gtfs_static_feeds`), it's rejected and reported to Sentry. Each entry must use one schema or the other.

Both v1 and v2 configurations accept one static feed and one realtime source per entry. See [Feed contract and migration](#feed-contract-and-migration) when upgrading a v2 entry that previously listed multiple feeds.

### Application Options

- **Fetch Interval** → default `30s` (`--fetch-interval <seconds>`)
- **Environment** → `development` (default), `staging`, `production` (`--env <value>`)
- **Port** → default `4000` (`--port <number>`)

⚠️ If running with **Docker Compose**, Prometheus runs on `9090`, Alertmanager on `9093`, and Grafana on `3000`. Don’t use those ports.

### Environment Variables

- **Sentry DSN**

```bash
    export SENTRY_DSN="your_sentry_dsn"
```

- **Config Auth (for remote configs)**

```bash
    export CONFIG_AUTH_USER="username"
    export CONFIG_AUTH_PASS="password"
```

## Running

It may take a few minutes for Watchdog to start exposing data to Prometheus, since initial setup includes tasks such as downloading the GTFS bundle.

### 1. Docker Compose (recommended)

Run Watchdog with **Prometheus, Alertmanager, and Grafana**:

```bash
docker compose up --build
```

Services:

- Watchdog → `4000`
- Prometheus → `9090`
- Alertmanager → `9093`
- Grafana → `3000`

Prometheus loads the alert rules in `prometheus/rules/` and forwards firing alerts to Alertmanager. The receiver in `prometheus/alertmanager.yml` is a placeholder that nothing serves, so replace it with your own webhook, Slack, PagerDuty, or email receiver before relying on notifications. Alertmanager keeps silences and its notification log in the `<project>_alertmanager-data` volume. Compose prefixes the project name, which defaults to the directory name, so a checkout in `watchdog/` uses `watchdog_alertmanager-data`. The volume survives container recreation; `docker compose down -v` removes it.

Stop services:

```bash
docker compose down
```

Restart services:

```bash
docker compose restart
```

Grafana auto-loads a Go runtime dashboard. Prometheus is pre-configured to scrape Watchdog.

See [Endpoints](#endpoints) to access metrics, health checks, Grafana, Prometheus, and Alertmanager.

### 2. Watchdog Only

#### Local Config

```bash
go run ./cmd/watchdog/ --config-file path/to/config.json
```

#### Remote Config (with auth)

```bash
go run ./cmd/watchdog/ \
  --config-url http://example.com/config.json
```

See [Endpoints](#endpoints) to access metrics and health checks.

### 3. Docker (single container)

#### Build image

```bash
docker build -t watchdog .
```

#### Run with local config

```bash
docker run -d \
  --name watchdog \
  -v ./config.json:/app/config.json \
  -p 4000:4000 \
  watchdog \
  --config-file /app/config.json
```

#### Run with remote config

```bash
docker run -d \
  --name watchdog \
  -e CONFIG_AUTH_USER=admin \
  -e CONFIG_AUTH_PASS=password \
  -p 4000:4000 \
  watchdog \
  --config-url http://example.com/config.json
```

See [Endpoints](#endpoints) to access metrics and health checks.

## Endpoints

During **development** (using `localhost`):

- Watchdog Metrics: [http://localhost:4000/metrics](http://localhost:4000/metrics)
- Watchdog Health Check: [http://localhost:4000/v1/healthcheck](http://localhost:4000/v1/healthcheck)
- Grafana: [http://localhost:3000/login](http://localhost:3000/login) → default user/pass: `admin` / `admin`
- Prometheus Targets: [http://localhost:9090/targets](http://localhost:9090/targets)
- Prometheus Query: [http://localhost:9090/query](http://localhost:9090/query)
- Alertmanager: [http://localhost:9093](http://localhost:9093)

During **production** (replace `<server-ip-or-domain>`):

- Watchdog Metrics: `http://<server-ip-or-domain>:4000/metrics`
- Watchdog Health Check: `http://<server-ip-or-domain>:4000/v1/healthcheck`
- Grafana: `http://<server-ip-or-domain>:3000/login`
- Prometheus Targets: `http://<server-ip-or-domain>:9090/targets`
- Prometheus Query: `http://<server-ip-or-domain>:9090/query`
- Alertmanager: `http://<server-ip-or-domain>:9093`

## Testing

### Unit Tests

```bash
go test ./...
```

### Integration Tests

- Copy `integration_config.json.template` → `integration_config.json`
- Fill in OBA server values
- Run:

```bash
go test -tags=integration ./internal/integration \
  -integration-config path/to/integration_config.json
```

Note:

- ⚠️ the file **must** be named `integration_config.json`
- It’s Git-ignored for safety

## Contributing

Refer to [CONTRIBUTING.md](./CONTRIBUTING.md) for detailed contribution guidelines.
