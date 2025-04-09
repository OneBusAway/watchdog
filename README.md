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

# Kubernetes Deployment

This section provides instructions to deploy the `watchdog` application using Kubernetes manifests.

## Steps to Deploy Watchdog

### 1. Create a Kind Cluster

Run the following command to create a Kubernetes cluster using Kind:

```bash
kind create cluster --name watchdog-kind
```

This will create a cluster named `watchdog-kind` and set the `kubectl` context to `kind-watchdog-kind`.

Verify the cluster information:

```bash
kubectl cluster-info --context kind-watchdog-kind
```

### 2. Create a Namespace

Create a namespace for the `watchdog` application:

```bash
kubectl create namespace watchdog
```

### 3. Apply Kubernetes Manifests

Deploy the ConfigMap, Deployment, and Service resources:

```bash
kubectl apply -f k8s/configmap.yaml -n watchdog
kubectl apply -f k8s/deployment.yaml -n watchdog
kubectl apply -f k8s/service.yaml -n watchdog
```

### 4. Verify Deployment

Check the status of the pods in the `watchdog` namespace:

```bash
kubectl get pods -n watchdog
```

You should see output similar to:

```
NAME                        READY   STATUS    RESTARTS   AGE
watchdog-5f4c467b49-hb6wk   1/1     Running   0          36s
watchdog-5f4c467b49-mdzbk   1/1     Running   0          36s
```

List all resources in the `watchdog` namespace:

```bash
kubectl get all -n watchdog
```

### 5. Access the Service

The `watchdog` service is exposed as a `ClusterIP` service on port `8080`. If the `Service` type is `ClusterIP`, you can use `kubectl port-forward` to access the service locally:

```bash
kubectl port-forward svc/watchdog 8080:8080 -n watchdog
```

This will forward the service to `localhost:8080`.

### 6. Delete the Cluster

When you're done, delete the Kind cluster:

```bash
kind delete cluster --name watchdog-kind
```

This will clean up all resources and remove the cluster.


# Testing

```
go test ./...
```
