package investigation

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/metrics"
)

// statusRecorder remembers the status code a handler sends. http.ResponseWriter
// has no way to read it back, and the request metrics need it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write sends the body. A handler that writes without calling WriteHeader
// gets 200 from net/http, so that is what is recorded.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// withMetrics counts and times every request under route, the route template
// (for example /v1/servers/:server_id), never the request path, so the number
// of series stays bounded. Rejected requests are counted too.
func withMetrics(route string, next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next(rec, r, ps)

		status := rec.status
		if status == 0 {
			// The handler wrote nothing; net/http sends 200.
			status = http.StatusOK
		}
		metrics.InvestigationAPIRequests.WithLabelValues(route, strconv.Itoa(status)).Inc()
		metrics.InvestigationAPIRequestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	}
}

// requireDatabase returns 503 when investigation storage is disabled or was
// unavailable at startup.
func (s *InvestigationService) requireDatabase(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if s.Pool == nil {
			writeProblem(w, http.StatusServiceUnavailable, "Investigation storage disabled",
				"The investigation database is disabled or was unavailable at startup.", nil)
			return
		}
		next(w, r, ps)
	}
}

// negotiate returns 406 when the Accept header allows no JSON response.
func negotiate(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if !acceptsJSON(r.Header.Values("Accept")) {
			writeProblem(w, http.StatusNotAcceptable, "Not acceptable",
				"This API only returns application/json.", nil)
			return
		}
		next(w, r, ps)
	}
}

// acceptsJSON reports whether the Accept header values allow
// application/json. No header, or only empty values, means anything is
// accepted. A media range with q=0 is refused, and malformed entries are
// skipped.
func acceptsJSON(values []string) bool {
	seen := false
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			seen = true
			mediaType, params, err := mime.ParseMediaType(part)
			if err != nil {
				continue
			}
			if q, ok := params["q"]; ok {
				if weight, err := strconv.ParseFloat(q, 64); err != nil || weight <= 0 {
					continue
				}
			}
			switch mediaType {
			case "application/json", "application/*", "*/*":
				return true
			}
		}
	}
	return !seen
}
