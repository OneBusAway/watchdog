package app

import (
	"encoding/json"
	"net/http"
)

// HealthStatus represents the structure of the health check response.
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
	app.Mu.RLock()
	numServers := len(app.Config.Servers)
	app.Mu.RUnlock()

	ready := numServers > 0 // Consider ready if at least one server is configured

	status := HealthStatus{
		Status:      "available",
		Environment: app.Config.Env,
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
