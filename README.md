# watchdog

[![Coverage Status](https://coveralls.io/repos/github/OneBusAway/watchdog/badge.svg?branch=main)](https://coveralls.io/github/OneBusAway/watchdog?branch=main)

Golang-based Watchdog service for OBA REST API servers

# Requirements

Go 1.23 or higher



## Configuration

The watchdog service can be configured using either:
- A **local JSON configuration file** (`--config-file`).
- A **remote JSON configuration URL** (`--config-url`).

### JSON Configuration Format

The JSON configuration file should contain an array of `ObaServer` objects. Example:

```json
[
  {
    "name": "Test Server",
    "id": 1,
    "oba_base_url": "https://test.example.com",
    "oba_api_key": "test-key",
    "gtfs_url": "https://gtfs.example.com",
    "trip_update_url": "https://trip.example.com",
    "vehicle_position_url": "https://vehicle.example.com",
    "gtfs_rt_api_key": "api-key",
    "gtfs_rt_api_value": "api-value"
  }
]
```

## Sentry Configuration

To enable Sentry error tracking, set the `SENTRY_DSN` environment variable with your Sentry DSN.

```sh
export SENTRY_DSN="your_sentry_dsn"
```

# Running

#### **Using a Local Configuration File**

```bash
go run ./cmd/watchdog/ --config-file /path/to/your/config.json
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

####  Run the Application with the Remote URL

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

```bash
docker run -d \
  --name watchdog \
  -e CONFIG_AUTH_USER=admin \
  -e CONFIG_AUTH_PASS=password \
  -p 3000:3000 \
  watchdog \
  --config-url http://example.com/config.json
```

# Testing

```
go test ./...
```
