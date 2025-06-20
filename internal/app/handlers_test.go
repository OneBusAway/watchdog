package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/server"
)

func TestHealthcheckHandler(t *testing.T) {
	// Create a new instance of our application struct which uses the mock env
	app := &Application{
		Config: server.Config{
			Env:     "testing",
			Servers: []models.ObaServer{{ID: 1, Name: "Test Server"}},
		},
		Version: "test-version",
	}

	// Create a new httptest.ResponseRecorder which satisfies the http.ResponseWriter
	// interface and records the response status code, headers and body.
	rr := httptest.NewRecorder()

	// Create a new http.Request instance for making the request
	request, err := http.NewRequest(http.MethodGet, "/v1/healthcheck", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Call the healthcheckHandler method to process the request
	app.healthcheckHandler(rr, request)

	// Check if the status code is what we expect
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			status, http.StatusOK)
	}

	var resp struct {
		Status      string `json:"status"`
		Environment string `json:"environment"`
		Version     string `json:"version"`
		Servers     int    `json:"servers"`
		Ready       bool   `json:"ready"`
	}

	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "available" {
		t.Errorf("expected status 'available', got %q", resp.Status)
	}
	if resp.Environment != "testing" {
		t.Errorf("expected environment 'testing', got %q", resp.Environment)
	}
	if resp.Version != "test-version" {
		t.Errorf("expected version 'test-version', got %q", resp.Version)
	}
	if resp.Servers != 1 {
		t.Errorf("expected servers 1, got %d", resp.Servers)
	}
	if !resp.Ready {
		t.Errorf("expected ready true, got false")
	}
}
