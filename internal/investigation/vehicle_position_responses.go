package investigation

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// kindVehicleResponse is the include kind of a vehicle-position response (E28).
const kindVehicleResponse resourceKind = "vehicle_response"

// vehiclePositionResponse is one vehicle-position response in a response
// body (E28).
type vehiclePositionResponse struct {
	VehiclePositionResponseID int64    `json:"vehicle_position_response_id"`
	EndpointID                int64    `json:"endpoint_id"`
	ObservedAt                apiTime  `json:"observed_at"`
	SourceHeaderTime          *apiTime `json:"source_header_time"`
	Incrementality            string   `json:"incrementality"`
	PayloadSha256             *string  `json:"payload_sha256"`
	Links                     links    `json:"links"`
}

// vehiclePositionResponseBody is the E28 body: the response, plus included
// when asked for. Embedding keeps the response's fields at the top level.
type vehiclePositionResponseBody struct {
	vehiclePositionResponse
	Included included `json:"included,omitzero"`
}

// responseIncludable is what include accepts on E28: a response links to
// one vehicle-position endpoint.
var responseIncludable = map[string]resourceKind{
	"endpoint": kindEndpoint,
}

// getVehiclePositionResponse serves
// GET /v1/vehicle-position-responses/:vehicle_position_response_id: one
// vehicle-position response by its ID. An unknown ID returns 404.
// Responses are immutable once recorded.
func (s *InvestigationService) getVehiclePositionResponse(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "vehicle_position_response_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, responseIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclePositionResponseBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehiclePositionResponse(ctx, id)
		if err != nil {
			return err
		}
		l := responseLinks(row)
		body.vehiclePositionResponse = responseRepresentation(row, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheImmutable(time.Time{}))
}

// responseLinks is everything one response links to that can be included:
// itself, its endpoint, and the evidence list.
func responseLinks(row dbgen.VehiclePositionResponse) itemLinks {
	self := "/v1/vehicle-position-responses/" + strconv.FormatInt(row.VehiclePositionResponseID, 10)
	return itemLinks{
		"self": related{
			Kind: kindVehicleResponse,
			ID:   row.VehiclePositionResponseID,
			URL:  self,
		},
		"endpoint": related{
			Kind: kindEndpoint,
			ID:   row.EndpointID,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(row.EndpointID, 10),
		},
		"evidence": related{
			URL: linkWithQuery("/v1/vehicle-position-evidence",
				url.Values{"response_id": {strconv.FormatInt(row.VehiclePositionResponseID, 10)}}),
		},
	}
}

// responseRepresentation maps one row to the representation that E28 returns.
func responseRepresentation(row dbgen.VehiclePositionResponse, l itemLinks) vehiclePositionResponse {
	var sourceHeaderTime *apiTime
	if row.SourceHeaderTime != nil {
		t := newAPITime(*row.SourceHeaderTime)
		sourceHeaderTime = &t
	}
	return vehiclePositionResponse{
		VehiclePositionResponseID: row.VehiclePositionResponseID,
		EndpointID:                row.EndpointID,
		ObservedAt:                newAPITime(row.ObservedAt),
		SourceHeaderTime:          sourceHeaderTime,
		Incrementality:            row.Incrementality,
		PayloadSha256:             row.PayloadSha256,
		Links:                     l.links(),
	}
}

// loadResponses is the include loader for kindResponse. It returns the E28
// representation of each response in ids, in one query.
func loadResponses(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.ListVehiclePositionResponsesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		l := responseLinks(row)
		out[row.VehiclePositionResponseID] = responseRepresentation(row, l)
	}
	return out, nil
}
