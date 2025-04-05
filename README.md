# watchdog

[![Coverage Status](https://coveralls.io/repos/github/OneBusAway/watchdog/badge.svg?branch=main)](https://coveralls.io/github/OneBusAway/watchdog?branch=main)

Golang-based Watchdog service for OBA REST API servers.

## Requirements

- Go 1.23 or higher


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

To enable Sentry error tracking, set the `SENTRY_DSN` environment variable with your Sentry DSN:

```sh
export SENTRY_DSN="your_sentry_dsn"
```

## Running

### Using a Local Configuration File

```bash
go run ./cmd/watchdog/ --config-file /path/to/your/config.json
```

### Using a Remote Configuration URL with Authentication

To load the configuration from a remote URL that requires basic authentication, follow these steps:

1. **Set the Required Environment Variables**  
   Before running the application, set the `CONFIG_AUTH_USER` and `CONFIG_AUTH_PASS` environment variables with the username and password for authentication.

   #### On Linux/macOS:

   ```bash
   export CONFIG_AUTH_USER="your_username"
   export CONFIG_AUTH_PASS="your_password"
   ```

   #### On Windows:

   ```bash
   set CONFIG_AUTH_USER=your_username
   set CONFIG_AUTH_PASS=your_password
   ```

2. **Run the Application with the Remote URL**  
   Use the `--config-url` flag to specify the remote configuration URL. For example:

   ```bash
   go run ./cmd/watchdog/ \
     --config-url http://example.com/config.json
   ```

## Running with Docker Compose

Before using Docker Compose, ensure it is installed. You can install Docker Compose by following the official guide:  
[Install Docker Compose](https://docs.docker.com/compose/install/)

1. **Run the Application**  
   Build and start the application using Docker Compose:

   ```bash
   docker-compose up --build
   ```

   This will build the Docker image and start the container using the configuration specified in the `compose.yaml` file.

2. **Stop the Application**  
   To stop the application, run:

   ```bash
   docker-compose down
   ```

## Building the Docker Image Directly

Before building the Docker image, ensure Docker is installed. You can install Docker by following the official guide:  
[Install Docker](https://docs.docker.com/get-docker/)

1. **Build the Docker Image**  
   Navigate to the root of the project directory and run:

   ```bash
   docker build -t watchdog .
   ```

2. **Run the Docker Container**  
   Start the container using the built image:

   ```bash
   docker run -d \
     --name watchdog \
     -e CONFIG_AUTH_USER=admin \
     -e CONFIG_AUTH_PASS=password \
     -p 4000:4000 \
     watchdog \
     --config-url http://example.com/config.json
   ```

## Testing

Run the tests to verify the application:

```bash
go test ./...
```
