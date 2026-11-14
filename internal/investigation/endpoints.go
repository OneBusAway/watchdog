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

// kindEndpoint is the include kind of a vehicle-position endpoint (E07).
// Every link named endpoint points to one.
const kindEndpoint resourceKind = "endpoint"

// vehiclePositionEndpoint is one vehicle-position feed URL in a response
// (E06, E07).
type vehiclePositionEndpoint struct {
	EndpointID                   int64   `json:"endpoint_id"`
	ServerID                     int64   `json:"server_id"`
	NormalizedVehiclePositionURL string  `json:"normalized_vehicle_position_url"`
	CreatedAt                    apiTime `json:"created_at"`
	// Configured is true when the server's current configuration still
	// fetches this URL, so problems from a retired feed are not blamed on
	// the current one.
	Configured bool  `json:"configured"`
	Links      links `json:"links"`
}

// vehiclePositionEndpointList is the E06 body. It is not paginated: a server
// has few feed URLs.
type vehiclePositionEndpointList struct {
	Items    []vehiclePositionEndpoint `json:"items"`
	Links    links                     `json:"links"`
	Included included                  `json:"included,omitzero"`
}

// endpointIncludable is what include accepts on E06 and E07: an endpoint
// links to one server.
var endpointIncludable = map[string]resourceKind{"server": kindServer}

// listServerVehiclePositionEndpoints serves
// GET /v1/servers/:server_id/vehicle-position-endpoints: every
// vehicle-position feed URL recorded for one server, by endpoint_id
// ascending, including URLs no longer configured. An unknown server is a
// 404, not an empty list.
func (s *InvestigationService) listServerVehiclePositionEndpoints(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	serverID, ok := pathID(ps, "server_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, endpointIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclePositionEndpointList
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		// GetServer returns pgx.ErrNoRows for an unknown server, which fail
		// turns into a 404. Its base URL selects the configuration entries
		// that decide configured.
		server, err := q.GetServer(ctx, serverID)
		if err != nil {
			return err
		}
		rows, err := q.ListServerVehiclePositionEndpoints(ctx, serverID)
		if err != nil {
			return err
		}
		configured := configuredVehiclePositionURLs(s.Servers(), server.NormalizedBaseUrl)
		items, related := endpointItems(rows, configured)
		body = vehiclePositionEndpointList{
			Items: items,
			Links: links{"self": linkWithQuery(r.URL.Path, r.URL.Query())},
		}
		body.Included, err = s.loadIncluded(ctx, q, related, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// configuredVehiclePositionURLs returns the normalized vehicle-position URL
// of every GTFS-RT feed configured for the server at baseURL (a normalized
// base URL). An agency-mode server has one configuration entry per agency,
// so the feeds of every entry for that server count.
func configuredVehiclePositionURLs(servers []models.ObaServer, baseURL string) map[string]bool {
	configured := map[string]bool{}
	for _, s := range servers {
		if utils.SanitizeServerURL(s.ObaBaseURL) != baseURL {
			continue
		}
		for _, feed := range s.GtfsRTFeeds {
			if u := utils.SanitizeServerURL(feed.VehiclePositionURL); u != "" {
				configured[u] = true
			}
		}
	}
	return configured
}

// endpointItems maps rows to their representations, and returns each item's
// links for loadIncluded. configured holds the normalized vehicle-position
// URLs the server's configuration fetches. The items are never nil, so an
// empty list encodes as [] rather than null.
func endpointItems(rows []dbgen.VehiclePositionEndpoint, configured map[string]bool) ([]vehiclePositionEndpoint, []itemLinks) {
	items := make([]vehiclePositionEndpoint, 0, len(rows))
	related := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := endpointLinks(row)
		items = append(items, endpointRepresentation(row, l, configured))
		related = append(related, l)
	}
	return items, related
}

// endpointLinks is everything one endpoint links to that can be included,
// or is included elsewhere: itself and its server.
func endpointLinks(row dbgen.VehiclePositionEndpoint) itemLinks {
	return itemLinks{
		"self": {
			Kind: kindEndpoint,
			ID:   row.EndpointID,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(row.EndpointID, 10),
		},
		"server": {
			Kind: kindServer,
			ID:   row.ServerID,
			URL:  "/v1/servers/" + strconv.FormatInt(row.ServerID, 10),
		},
	}
}

// vehiclePositionEndpointBody is the E07 body: the endpoint, plus included
// when asked for. Embedding keeps the endpoint's fields at the top level.
type vehiclePositionEndpointBody struct {
	vehiclePositionEndpoint
	Included included `json:"included,omitzero"`
}

// getVehiclePositionEndpoint serves GET /v1/vehicle-position-endpoints/:endpoint_id:
// one vehicle-position feed endpoint by its ID. An unknown ID returns 404.
func (s *InvestigationService) getVehiclePositionEndpoint(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "endpoint_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, endpointIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclePositionEndpointBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehiclePositionEndpoint(ctx, id)
		if err != nil {
			return err
		}
		server, err := q.GetServer(ctx, row.ServerID)
		if err != nil {
			return err
		}
		configured := configuredVehiclePositionURLs(s.Servers(), server.NormalizedBaseUrl)
		l := endpointLinks(row)
		body.vehiclePositionEndpoint = endpointRepresentation(row, l, configured)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}

// loadEndpoints is the include loader for kindEndpoint. It returns the E07
// representation of each endpoint in ids, using two queries: one for the
// endpoints and one for their parent servers. Each endpoint's configured flag
// is computed from its own server's configuration entry, matching exactly what
// a direct GET on that endpoint would return.
func (s *InvestigationService) loadEndpoints(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListVehiclePositionEndpointsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Collect distinct server_ids so we can fetch them in one query.
	seen := map[int64]bool{}
	serverIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if !seen[row.ServerID] {
			seen[row.ServerID] = true
			serverIDs = append(serverIDs, row.ServerID)
		}
	}
	servers, err := q.ListServersByIDs(ctx, serverIDs)
	if err != nil {
		return nil, err
	}
	configuredByServer := make(map[int64]map[string]bool, len(servers))
	for _, srv := range servers {
		configuredByServer[srv.ServerID] = configuredVehiclePositionURLs(s.Servers(), srv.NormalizedBaseUrl)
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		l := endpointLinks(row)
		out[row.EndpointID] = endpointRepresentation(row, l, configuredByServer[row.ServerID])
	}
	return out, nil
}

// endpointRepresentation maps one row to the representation that E06 lists
// and E07 returns.
func endpointRepresentation(row dbgen.VehiclePositionEndpoint, l itemLinks, configured map[string]bool) vehiclePositionEndpoint {
	out := l.links()
	out["vehicle_anomaly_episodes"] = linkWithQuery("/v1/vehicle-anomaly-episodes",
		url.Values{"endpoint_id": {strconv.FormatInt(row.EndpointID, 10)}})
	return vehiclePositionEndpoint{
		EndpointID:                   row.EndpointID,
		ServerID:                     row.ServerID,
		NormalizedVehiclePositionURL: row.NormalizedVehiclePositionUrl,
		CreatedAt:                    newAPITime(row.CreatedAt),
		Configured:                   configured[row.NormalizedVehiclePositionUrl],
		Links:                        out,
	}
}
