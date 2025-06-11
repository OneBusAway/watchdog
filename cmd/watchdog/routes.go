package main

import (
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"

	"github.com/julienschmidt/httprouter"
)

func (app *application) routes() http.Handler {
	// Initialize a new httprouter router instance.
	router := httprouter.New()

	// Register the relevant methods, URL patterns and handler functions for our
	// endpoints using the HandlerFunc() method. Note that http.MethodGet and
	// http.MethodPost are constants which equate to the strings "GET" and "POST"
	// respectively.
	router.HandlerFunc(http.MethodGet, "/v1/healthcheck", app.healthcheckHandler)
	router.Handler(http.MethodGet, "/metrics", promhttp.Handler())

	// Wrap router with Sentry middleware
	// Return wrapped httprouter instance.
	return sentryMiddleware(router)
}
