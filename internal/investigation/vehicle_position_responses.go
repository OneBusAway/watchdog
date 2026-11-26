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

// vehiclePositionResponseIncludable maps each link that can be included (E28) to
// its kind. The endpoint loader comes from #221.
var vehiclePositionResponseIncludable = map[string]resourceKind{
	"endpoint": kindEndpoint,
}

// vehiclePositionResponse is the representation of one stored feed response (E28).
type vehiclePositionResponse struct {
	VehiclePositionResponseID int64    `json:"vehicle_position_response_id"`
	EndpointID                int64    `json:"endpoint_id"`
	ObservedAt                apiTime  `json:"observed_at"`
	SourceHeaderTime          *apiTime `json:"source_header_time"`
	Incrementality            string   `json:"incrementality"`
	PayloadSHA256             *string  `json:"payload_sha256"`
	Links                     links    `json:"links"`
}

// vehiclePositionResponseBody is the E28 response.
type vehiclePositionResponseBody struct {
	vehiclePositionResponse
	Included included `json:"included,omitzero"`
}

// vehiclePositionResponseLinks is everything one response links to. Both links
// are always present.
func vehiclePositionResponseLinks(row dbgen.VehiclePositionResponse) itemLinks {
	return itemLinks{
		"self": {URL: "/v1/vehicle-position-responses/" + strconv.FormatInt(row.VehiclePositionResponseID, 10)},
		"endpoint": {
			Kind: kindEndpoint,
			ID:   row.EndpointID,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(row.EndpointID, 10),
		},
	}
}

// vehiclePositionResponseRepresentation maps one row to its E28 representation.
func vehiclePositionResponseRepresentation(row dbgen.VehiclePositionResponse, l itemLinks) vehiclePositionResponse {
	return vehiclePositionResponse{
		VehiclePositionResponseID: row.VehiclePositionResponseID,
		EndpointID:                row.EndpointID,
		ObservedAt:                newAPITime(row.ObservedAt),
		SourceHeaderTime:          newOptionalAPITime(row.SourceHeaderTime),
		Incrementality:            row.Incrementality,
		PayloadSHA256:             row.PayloadSha256,
		Links:                     l.links(),
	}
}

// getVehiclePositionResponse serves GET /v1/vehicle-position-responses/:response_id (E28).
func (s *InvestigationService) getVehiclePositionResponse(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "response_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, vehiclePositionResponseIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclePositionResponseBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.GetVehiclePositionResponse(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return pgx.ErrNoRows
		}
		l := vehiclePositionResponseLinks(rows[0])
		body.vehiclePositionResponse = vehiclePositionResponseRepresentation(rows[0], l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A stored feed response never changes, so it is immutable without include.
	// With include, the response is only as fresh as its included parts, which
	// can change, so it gets the short policy (decision on #412).
	cache := cacheImmutable(time.Time{})
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}

// loadVehiclePositionResponses is the include loader for kindVehicleResponse. It
// returns each response's E28 representation, keyed by its ID, in one query.
func loadVehiclePositionResponses(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.GetVehiclePositionResponse(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[row.VehiclePositionResponseID] = vehiclePositionResponseRepresentation(row, vehiclePositionResponseLinks(row))
	}
	return out, nil
}
