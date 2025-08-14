# watchdog

[![Coverage Status](https://coveralls.io/repos/github/OneBusAway/watchdog/badge.svg?branch=main)](https://coveralls.io/github/OneBusAway/watchdog?branch=main)

Golang-based Watchdog service for OBA REST API servers, providing deep observability by exposing a rich suite of Prometheus metrics. These metrics enable comprehensive monitoring of API uptime, GTFS Static and GTFS-RT data integrity, vehicle telemetry, agency and stop coverage, and overall operational health.

You can find documentation for the currently exposed metrics along with an interpretation guide [here](./docs/METRICS.md).

# Requirements

Go 1.23 or higher

## Configuration

The watchdog service can be configured using either:

- A **local JSON configuration file** (`--config-file`).
- A **remote JSON configuration URL** (`--config-url`).
  - Using a remote configuration allows you to dynamically add, remove, or update server configurations at runtime.

### JSON Configuration Format

The JSON configuration file should contain an array of `ObaServer` objects, each representing an OBA server to be monitored. Example:

```json
[
  {
    "name": "Test Server 1",
    "id": 1,
    "oba_base_url": "https://test1.example.com",
    "oba_api_key": "test-key-1",
    "gtfs_url": "https://gtfs1.example.com",
    "trip_update_url": "https://trip1.example.com",
    "vehicle_position_url": "https://vehicle1.example.com",
    "gtfs_rt_api_key": "api-key-1",
    "gtfs_rt_api_value": "api-value-1",
    "agency_id": "agency-1"
  },
  {
    "name": "Test Server 2",
    "id": 2,
    "oba_base_url": "https://test2.example.com",
    "oba_api_key": "test-key-2",
    "gtfs_url": "https://gtfs2.example.com",
    "trip_update_url": "https://trip2.example.com",
    "vehicle_position_url": "https://vehicle2.example.com",
    "gtfs_rt_api_key": "api-key-2",
    "gtfs_rt_api_value": "api-value-2",
    "agency_id": "agency-2"
  }
]
```

### Local Configuration Setup

1. Either copy or rename `config.json.template` to `config.json` in the same folder.
2. Update `config.json` with your OBA server values.

Note: The `config.json` file is ignored by Git to prevent accidental commits of sensitive configuration data.

### Remote Configuration Setup

1. Create a `config.json` file based on the `config.json.template` format.
2. Fill in your OBA server values in `config.json`.
3. Host it publicly and point the app to its URL.

## Sentry Configuration

To enable Sentry error tracking, set the `SENTRY_DSN` environment variable with your Sentry DSN.

```sh
export SENTRY_DSN="your_sentry_dsn"
```

# Running

#### **Using a Local Configuration File**

```bash
go run ./cmd/watchdog/ --config-file ./config.json
```

## **Using a Remote Configuration URL with Authentication**

To load the configuration from a remote URL that requires basic authentication, follow these steps:

### 1. **Set the Required Environment Variables**

Before running the application, set the `CONFIG_AUTH_USER` and `CONFIG_AUTH_PASS` environment variables with the username and password for authentication.

#### On Linux/macOS:

```bash
export CONFIG_AUTH_USER="your_username"
export CONFIG_AUTH_PASS="your_password"
```

#### On Windows

```bash
set CONFIG_AUTH_USER=your_username
set CONFIG_AUTH_PASS=your_password
```

#### Run the Application with the Remote URL

Use the --config-url flag to specify the remote configuration URL. For example:

```bash
go run ./cmd/watchdog/ \
  --config-url http://example.com/config.json
```

## **Running with Docker**

You can also run the application using Docker. Here’s how:

### 1. **Build the Docker Image**

First, build the Docker image for the application. Navigate to the root of the project directory and run:

```bash
docker build -t watchdog .
```

### 2. **Run the Docker Container**

#### 2.1 **Using Local Config**

```bash
docker run -d \
  --name watchdog \
  -e CONFIG_AUTH_USER=admin \
  -e CONFIG_AUTH_PASS=password \
  -v ./config.json:/app/config.json \
  -p 4000:4000 \
  watchdog \
  --config-file /app/config.json
```

#### 2.2 **Using Remote Config**

```bash
docker run -d \
  --name watchdog \
  -e CONFIG_AUTH_USER=admin \
  -e CONFIG_AUTH_PASS=password \
  -p 4000:4000 \
  watchdog \
  --config-url http://example.com/config.json
```

# Testing

### To run all unit test cases:

```bash
go test ./...
```

### To run integration testing

Follow these steps:

1. Open the file `integration_servers.json.template` inside the `internal/integration` package.
2. Rename it to `integration_servers.json`.
3. Fill in your OBA server configuration values.

Then run:

```bash
go test -tags=integration ./internal/integration -integration-config ./integration_servers.json
```

Note:

- The `integration_servers.json` file is ignored by Git to prevent accidental commits of sensitive data.
- You can point to any config file by passing its path using the -integration-config flag.
