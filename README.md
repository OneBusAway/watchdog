# Watchdog

[![Coverage Status](https://coveralls.io/repos/github/OneBusAway/watchdog/badge.svg?branch=main)](https://coveralls.io/github/OneBusAway/watchdog?branch=main)

**Watchdog** is a Go-based service that monitors [OneBusAway (OBA)](https://onebusaway.org/) REST API servers.
It exposes a comprehensive set of **Prometheus metrics** for monitoring:

- GTFS Static and GTFS-RT data integrity
- Vehicle telemetry
- Agency and stop coverage
- Overall operational health
  See the full list of metrics and interpretation guide [here](./docs/METRICS.md)

## Requirements

- **Go 1.23+**

## Setup

### Configuration

Watchdog requires a configuration file (`config.json`) before running. Even placeholder data is necessary to start the service.

#### Example `config.json`

```json
[
  {
    "server_name": "Test Server 1",
    "agency_name": "Test Agency 1",
    "agency_id": "agency-1",
    "oba_base_url": "https://test1.example.com",
    "oba_api_key": "test-key-1",
    "gtfs_static_feeds": ["https://gtfs1.example.com"],
    "gtfs_rt_feeds": [{
      "trip_update_url": "https://trip1.example.com",
      "vehicle_position_url": "https://vehicle1.example.com",
      "gtfs_rt_api_key": "api-key-1",
      "gtfs_rt_api_value": "api-value-1",
      "agency_ids": ["agency-1"]
    }]
  }
]
```

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

### Backward Compatibility (v1 → v2)

Watchdog used to accept a flat, single-server config schema. Legacy (v1) configs are still supported: they're **silently converted** to the current array-based schema (v2) at load time, so upgrading doesn't require changing your config or interrupt monitoring.

When a v1 entry is loaded, Watchdog maps it like this:

- `name` → `agency_name`
- `gtfs_url` → a single-entry `gtfs_static_feeds`
- `vehicle_position_url` / `trip_update_url` → the matching `gtfs_rt_feeds` entries
- `gtfs_rt_api_key` / `gtfs_rt_api_value` → the per-feed auth fields
- `id` → ignored

One caveat: v1 and v2 fields can't be mixed in the same entry. If an entry contains both schemas (e.g. a legacy `gtfs_url` alongside `gtfs_static_feeds`), it's rejected and reported to Sentry. Each entry must use one schema or the other.

Migrating to v2 is recommended whenever convenient — it's the only way to configure multiple static or RT feeds per agency. v1 supports just one of each.

### Server vs. agency scoping

Every entry in `config.json` is server-scoped at the top level: `server_name` is required and identifies the OBA deployment; the operator lists the static feeds each server exposes. `agency_id` is optional and controls the *scope* of the entry:

- **Agency-mode entry** — `agency_id` is set. Watchdog monitors only that one agency. Today's per-agency pipeline runs once per tick for that agency. `agency_name` is required (paired with `agency_id`).

- **Server-mode entry** — `agency_id` is absent. Watchdog probes `/api/where/metrics.json` every tick to learn which agencies OBA is currently serving, cross-references the live agency IDs against the static feeds' `agency.txt` declarations, and runs the per-agency pipeline for every agency that has BOTH a static bundle AND is reported as currently live.

Multi-agency static feeds are accepted: a single bundle pointer-shared across the serverKeys of every agency declared in its `agency.txt`. Static feeds whose `agency.txt` is empty, declares multiple agencies ambiguously, or whose declared agency isn't currently reported by OBA are reported to Sentry (`gtfs_static_feed_attribution_status` flips to 0) and the per-agency pipeline is skipped — but the bundle is still downloaded and parsed so the introspection metrics (`gtfs_static_stops_count`, `gtfs_static_routes_count`) keep reporting.

#### Liveness signals

The two metrics below are the operator's view into server-mode health:

- `gtfs_static_agency_currently_live{agency_id, agency_name, server_name, server_url}` — 1 if the agency has a static bundle AND is in `/api/where/metrics.json` `entry.AgencyIDs` in the current scrape; 0 otherwise.
- `gtfs_static_feed_attribution_status{feed_url, agency_id, agency_name, server_name, server_url}` — 1 if the feed was successfully attributed to a server-reported agency; 0 otherwise.

#### Vehicle attribution in server-mode

In agency-mode every RT vehicle is labeled with the configured `agency_id`. In server-mode one RT feed may carry vehicles from multiple agencies, so Watchdog attributes each vehicle by looking up its `TripDescriptor`'s `route_id` in a per-server `route_id → agency_id` index built from `routes.txt` at static-download time. Vehicles whose `route_id` is empty or unknown (and vehicles carrying no vehicle ID at all) are left out of the per-vehicle series and counted in `gtfs_rt_unattributed_vehicles_count{server_name, server_url}` so operators can detect static feeds that don't cover every RT route. The data-quality gauges `gtfs_rt_invalid_vehicle_coordinates` and `gtfs_rt_stopped_out_of_bounds_vehicles` instead file those vehicles under the server-scoped series (empty `agency_id`), so their per-agency series always sum to the server-wide count. See `docs/METRICS.md` for details.

#### Backward compatibility

This is a **deliberate breaking change** to the existing v2 format. Existing v2 entries without `server_name` become invalid — operators must add the field. The legacy v1 array-of-flat-objects format continues to work: `id` (int) is still ignored, `name` is repurposed as `server_name`, and a v1 entry with `agency_id` populates `agency_name` from `name` so the entry remains agency-scoped.

### Application Options

- **Fetch Interval** → default `30s` (`--fetch-interval <seconds>`)
- **Environment** → `development` (default), `staging`, `production` (`--env <value>`)
- **Port** → default `4000` (`--port <number>`)

⚠️ If running with **Docker Compose**, Prometheus runs on `9090` and Grafana on `3000`. Don’t use those ports.

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

Run Watchdog with **Prometheus + Grafana**:

```bash
docker compose up --build
```

Services:

- Watchdog → `4000`
- Prometheus → `9090`
- Grafana → `3000`

Stop services:

```bash
docker compose down
```

Restart services:

```bash
docker compose restart
```

Grafana auto-loads a Go runtime dashboard. Prometheus is pre-configured to scrape Watchdog.

See [Endpoints](#endpoints) to access metrics, health checks, Grafana, and Prometheus.

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

During **production** (replace `<server-ip-or-domain>`):

- Watchdog Metrics: `http://<server-ip-or-domain>:4000/metrics`
- Watchdog Health Check: `http://<server-ip-or-domain>:4000/v1/healthcheck`
- Grafana: `http://<server-ip-or-domain>:3000/login`
- Prometheus Targets: `http://<server-ip-or-domain>:9090/targets`
- Prometheus Query: `http://<server-ip-or-domain>:9090/query`

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
