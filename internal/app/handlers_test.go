package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"watchdog.onebusaway.org/internal/server"
)

func TestHealthcheckHandler(t *testing.T) {
	// Create a new instance of our application struct which uses the mock env
	app := &Application{
		Config: server.Config{
			Env: "testing",
		},
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

	// Check the response body contains the expected strings
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatal(err)
	}

	expectedStrings := []string{
		"status: available",
		"environment: testing",
		"version: " + app.Version,
	}

	for _, str := range expectedStrings {
		if !strings.Contains(string(body), str) {
			t.Errorf("handler returned unexpected body: got %v want to contain %v",
				string(body), str)
		}
	}
}
