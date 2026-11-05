package investigation

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// server is one OBA server in a response (E02).
type server struct {
	ServerID          int64   `json:"server_id"`
	NormalizedBaseURL string  `json:"normalized_base_url"`
	DisplayName       string  `json:"display_name"`
	CreatedAt         apiTime `json:"created_at"`
	// Configured is true when the current configuration still monitors this
	// server, so an investigator can tell a retired server in old history
	// from a live one.
	Configured bool  `json:"configured"`
	Links      links `json:"links"`
}

// serverList is the E02 body. It is not paginated: the number of servers is
// bounded by the configuration.
type serverList struct {
	Items []server `json:"items"`
	Links links    `json:"links"`
}

// listServers serves GET /v1/servers: every recorded OBA server, by server_id
// ascending. ?base_url= finds the one server stored under that URL, which is
// how a client turns a URL into the server_id other endpoints need.
func (s *InvestigationService) listServers(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "base_url")
	baseURL := paramBaseURL(values, "base_url", &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var rows []dbgen.OnebusawayServer
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		rows, err = q.ListServers(ctx, baseURL)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	body := serverList{
		Items: serverItems(rows, configuredServerURLs(s.Servers())),
		Links: links{"self": linkWithQuery(r.URL.Path, r.URL.Query())},
	}
	s.writeJSON(w, r, body, cacheShort())
}

// paramBaseURL reads a server URL and normalizes it with
// utils.SanitizeServerURL, the function that fills normalized_base_url. A
// value that normalizes to nothing (no host) is a problem, not "no filter".
func paramBaseURL(values url.Values, name string, bad *invalidParams) *string {
	raw := paramString(values, name, bad)
	if raw == nil {
		return nil
	}
	normalized := utils.SanitizeServerURL(*raw)
	if normalized == "" {
		bad.add(name, "must be a URL with a host, for example https://api.pugetsound.onebusaway.org")
		return nil
	}
	return &normalized
}

// configuredServerURLs returns the normalized base URL of every server in the
// configuration.
func configuredServerURLs(servers []models.ObaServer) map[string]bool {
	configured := make(map[string]bool, len(servers))
	for _, s := range servers {
		if u := utils.SanitizeServerURL(s.ObaBaseURL); u != "" {
			configured[u] = true
		}
	}
	return configured
}

// serverItems maps rows to their representations. configured holds the
// normalized base URLs in the current configuration. The result is never nil,
// so an empty list encodes as [] rather than null.
func serverItems(rows []dbgen.OnebusawayServer, configured map[string]bool) []server {
	items := make([]server, 0, len(rows))
	for _, row := range rows {
		id := strconv.FormatInt(row.ServerID, 10)
		self := "/v1/servers/" + id
		items = append(items, server{
			ServerID:          row.ServerID,
			NormalizedBaseURL: row.NormalizedBaseUrl,
			DisplayName:       row.DisplayName,
			CreatedAt:         newAPITime(row.CreatedAt),
			Configured:        configured[row.NormalizedBaseUrl],
			Links: links{
				"self":                       self,
				"agencies":                   self + "/agencies",
				"vehicle_position_endpoints": self + "/vehicle-position-endpoints",
				"gtfs_static_versions":       linkWithQuery("/v1/gtfs-static-versions", url.Values{"server_id": {id}}),
			},
		})
	}
	return items
}
