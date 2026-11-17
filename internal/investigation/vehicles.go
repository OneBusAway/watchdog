package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// vehicleIncludable uses the kind the vehicle-position-endpoint detail endpoint
// registers. Until it does, parseInclude rejects the name.
var vehicleIncludable = map[string]resourceKind{
	"endpoint": "vehicle_position_endpoint",
}

// vehicle is the representation of one vehicle identity (E21, E22).
type vehicle struct {
	VehicleEntityID int64  `json:"vehicle_entity_id"`
	EndpointID      int64  `json:"endpoint_id"`
	VehicleID       string `json:"vehicle_id"`
	Links           links  `json:"links"`
}

// vehiclesBody is the E21 response.
type vehiclesBody struct {
	Items    []vehicle `json:"items"`
	Links    links     `json:"links"`
	Included included  `json:"included,omitzero"`
}

// vehicleBody is the E22 response: one vehicle, with the same fields and links
// it carries as a list item.
type vehicleBody struct {
	vehicle
	Included included `json:"included,omitzero"`
}

// vehicleLinks builds one vehicle's links. Every one of them always exists:
// the endpoint is a NOT NULL reference, and the episode list is a valid
// collection even when the vehicle has no episodes.
func vehicleLinks(row dbgen.VehicleEntity) itemLinks {
	self := "/v1/vehicles/" + strconv.FormatInt(row.VehicleEntityID, 10)
	return itemLinks{
		"self": {URL: self},
		"endpoint": {
			Kind: "vehicle_position_endpoint",
			ID:   row.EndpointID,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(row.EndpointID, 10),
		},
		"anomaly_episodes": {URL: self + "/anomaly-episodes"},
	}
}

// vehicleRepresentation maps a database row to its API representation.
func vehicleRepresentation(row dbgen.VehicleEntity, l itemLinks) vehicle {
	return vehicle{
		VehicleEntityID: row.VehicleEntityID,
		EndpointID:      row.EndpointID,
		VehicleID:       row.ExternalVehicleID,
		Links:           l.links(),
	}
}

// vehicleItems maps rows to their representations, and returns each item's
// links alongside them because loadIncluded needs the kinds they point at.
// The items are never nil, so an empty list encodes as [] rather than null.
func vehicleItems(rows []dbgen.VehicleEntity) ([]vehicle, []itemLinks) {
	items := make([]vehicle, 0, len(rows))
	all := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := vehicleLinks(row)
		all = append(all, l)
		items = append(items, vehicleRepresentation(row, l))
	}
	return items, all
}

// listVehicles serves GET /v1/vehicles: the vehicles carrying one feed vehicle
// ID, on a single endpoint or anywhere on a server. A feed vehicle ID is only
// unique within one endpoint, so a scope is required rather than optional.
// Not paginated.
func (s *InvestigationService) listVehicles(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "vehicle_id", "endpoint_id", "server_id", "include")
	params := dbgen.ListVehiclesParams{
		EndpointID: paramID(values, "endpoint_id", &bad),
		ServerID:   paramID(values, "server_id", &bad),
	}
	if vehicleID := paramString(values, "vehicle_id", &bad); vehicleID != nil {
		params.ExternalVehicleID = *vehicleID
	} else if !values.Has("vehicle_id") {
		bad.add("vehicle_id", "is required")
	}
	if values.Has("endpoint_id") == values.Has("server_id") {
		const reason = "exactly one of endpoint_id and server_id is required"
		bad.add("endpoint_id", reason)
		bad.add("server_id", reason)
	}
	inc := s.parseInclude(values, vehicleIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclesBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.ListVehicles(ctx, params)
		if err != nil {
			return err
		}
		var all []itemLinks
		body.Items, all = vehicleItems(rows)
		body.Links = links{"self": linkWithQuery(r.URL.Path, r.URL.Query())}
		body.Included, err = s.loadIncluded(ctx, q, all, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.writeJSON(w, r, body, cacheShort())
}

// getVehicle serves GET /v1/vehicles/:vehicle_entity_id: one vehicle identity.
// A path ID that is not a positive integer is a 404 rather than a 400, because
// no such vehicle can exist.
func (s *InvestigationService) getVehicle(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "vehicle_entity_id")
	if !ok {
		s.fail(w, r, pgx.ErrNoRows)
		return
	}

	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, vehicleIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehicle(ctx, id)
		if err != nil {
			return err
		}
		l := vehicleLinks(row)
		body.vehicle = vehicleRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A vehicle identity is immutable once recorded, but included resources
	// can change. The identity has no Last-Modified because its columns are
	// never updated.
	cache := cacheShort()
	if !inc.requested() {
		cache = cacheImmutable(time.Time{})
	}
	s.writeJSON(w, r, body, cache)
}

// loadVehicles is the batched loader behind include=vehicle on the endpoints
// that link here. It reads every requested ID in one query and returns exactly
// this endpoint's representation for each.
func (s *InvestigationService) loadVehicles(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListVehiclesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	reps := make(map[int64]any, len(rows))
	for _, row := range rows {
		reps[row.VehicleEntityID] = vehicleRepresentation(row, vehicleLinks(row))
	}
	return reps, nil
}
