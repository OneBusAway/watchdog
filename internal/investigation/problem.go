package investigation

import (
	"encoding/json"
	"net/http"
)

// problemType is the problem+json "type" for every error. It links to the
// conventions section of the design doc.
const problemType = "https://github.com/OneBusAway/watchdog/blob/main/docs/investigation-api.md#3-conventions"

// invalidParam is one entry of a problem's invalid_params.
type invalidParam struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// problem is an RFC 9457 problem+json body.
type problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail"`
	InvalidParams []invalidParam `json:"invalid_params,omitempty"`
}

// writeProblem writes an application/problem+json error. invalid_params is
// included only when invalid is not empty.
func writeProblem(w http.ResponseWriter, status int, title, detail string, invalid []invalidParam) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:          problemType,
		Title:         title,
		Status:        status,
		Detail:        detail,
		InvalidParams: invalid,
	})
}
