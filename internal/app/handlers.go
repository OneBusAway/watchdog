package app

import (
	"encoding/json"
	"net/http"
)

// HealthStatus defines the structure of the JSON response returned by the
// application's health check endpoint (/v1/healthcheck).
//
// It provides metadata about the application's current operational status,
// including availability, deployment context, versioning, and runtime readiness.
// This structure is used to inform load balancers, orchestration tools (e.g., Kubernetes),
// monitoring systems, and operators about the application's health and deployability.
//
// Fields:
//   - Status: A high-level indicator of service availability (e.g., "available").
//   - Environment: The current environment in which the app is running (e.g., "development", "staging","production").
//   - Version: The application version string, useful for deployment tracking.
//   - Servers: The number of OBA (OneBusAway) backend servers currently configured and used.
//   - Ready: A boolean flag indicating whether the application is ready to serve traffic.
//     The application is considered "ready" if at least one backend server is configured.
//
// This struct is constructed and serialized to JSON by the `healthcheckHandler`,
// and it plays a central role in operational observability and readiness checks.
type HealthStatus struct {
	Status      string `json:"status"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Servers     int    `json:"servers"`
	Ready       bool   `json:"ready"`
}

// healthcheckHandler responds with a JSON representation of the application's health status.
//
// The response includes the application's availability status, environment, version,
// number of configured servers, and readiness (true if at least one server is configured).
// If no servers are configured (i.e., the application is not ready), the handler responds
// with HTTP 500 Internal Server Error; otherwise, it responds with HTTP 200 OK.

func (app *Application) healthcheckHandler(w http.ResponseWriter, r *http.Request) {
	numServers := len(app.ConfigService.Config.GetServers())

	ready := numServers > 0 // Consider ready if at least one server is configured

	status := HealthStatus{
		Status:      "available",
		Environment: app.ConfigService.Config.Env,
		Version:     app.Version,
		Servers:     numServers,
		Ready:       ready,
	}

	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusInternalServerError)
	}
	json.NewEncoder(w).Encode(status)
}
