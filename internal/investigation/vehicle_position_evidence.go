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

const (
	kindVehicleResponse      resourceKind = "vehicle_response"
	kindBoundingBoxVersion   resourceKind = "bounding_box_version"
	kindVehicleEntityVehicle resourceKind = "vehicle"
)

// vehiclePositionEvidenceIncludable maps each link that can be included (E27)
// to its kind. A name is accepted only once its kind has a loader; until then
// it returns 400.
var vehiclePositionEvidenceIncludable = map[string]resourceKind{
	"response":             kindVehicleResponse,
	"vehicle":              kindVehicleEntityVehicle,
	"gtfs_static_version":  kindGTFSStaticVersion,
	"bounding_box_version": kindBoundingBoxVersion,
}

// vehiclePositionEvidence is the representation of one evidence record (E27).
// Every value is the stored value; nullable fields encode as null.
type vehiclePositionEvidence struct {
	VehiclePositionEvidenceID int64    `json:"vehicle_position_evidence_id"`
	ObservedAt                apiTime  `json:"observed_at"`
	Incrementality            string   `json:"incrementality"`
	FeedEntityID              *string  `json:"feed_entity_id"`
	AgencyID                  *int64   `json:"agency_id"`
	AttributionReason         *string  `json:"attribution_reason"`
	VehicleSourceTime         *apiTime `json:"vehicle_source_time"`
	Latitude                  *float64 `json:"latitude"`
	Longitude                 *float64 `json:"longitude"`
	PositionValid             bool     `json:"position_valid"`
	PositionValidationReason  *string  `json:"position_validation_reason"`
	ReportedSpeedMps          *float64 `json:"reported_speed_mps"`
	ComputedSpeedMps          *float64 `json:"computed_speed_mps"`
	SpeedDiscrepancyRatio     *float64 `json:"speed_discrepancy_ratio"`
	CurrentStatus             *int64   `json:"current_status"`
	TripID                    *string  `json:"trip_id"`
	RouteID                   *string  `json:"route_id"`
	TripStartDate             *apiDate `json:"trip_start_date"`
	TripStartTimeSeconds      *int64   `json:"trip_start_time_seconds"`
	ScheduleRelationship      *string  `json:"schedule_relationship"`
	ExpectedScheduleState     *string  `json:"expected_schedule_state"`
	ExpectedScheduleReason    *string  `json:"expected_schedule_reason"`
	ExpectedWindowStart       *apiTime `json:"expected_window_start"`
	ExpectedWindowEnd         *apiTime `json:"expected_window_end"`
	Links                     links    `json:"links"`
}

// vehiclePositionEvidenceBody is the E27 response.
type vehiclePositionEvidenceBody struct {
	vehiclePositionEvidence
	Included included `json:"included,omitzero"`
}

// vehiclePositionEvidenceLinks is everything one evidence record links to.
// The response link is always present; the others only when their IDs are set.
func vehiclePositionEvidenceLinks(row dbgen.GetVehiclePositionEvidenceRow) itemLinks {
	self := "/v1/vehicle-position-evidence/" + strconv.FormatInt(row.VehiclePositionEvidenceID, 10)
	l := itemLinks{
		"self": {URL: self},
		"response": {
			Kind: kindVehicleResponse,
			ID:   row.VehiclePositionResponseID,
			URL:  "/v1/vehicle-position-responses/" + strconv.FormatInt(row.VehiclePositionResponseID, 10),
		},
	}
	if row.VehicleEntityID != nil {
		l["vehicle"] = related{
			Kind: kindVehicleEntityVehicle,
			ID:   *row.VehicleEntityID,
			URL:  "/v1/vehicles/" + strconv.FormatInt(*row.VehicleEntityID, 10),
		}
	}
	if row.GtfsStaticVersionID != nil {
		l["gtfs_static_version"] = related{
			Kind: kindGTFSStaticVersion,
			ID:   *row.GtfsStaticVersionID,
			URL:  "/v1/gtfs-static-versions/" + strconv.FormatInt(*row.GtfsStaticVersionID, 10),
		}
	}
	if row.BoundingBoxVersionID != nil {
		l["bounding_box_version"] = related{
			Kind: kindBoundingBoxVersion,
			ID:   *row.BoundingBoxVersionID,
			URL:  "/v1/bounding-box-versions/" + strconv.FormatInt(*row.BoundingBoxVersionID, 10),
		}
	}
	return l
}

// vehiclePositionEvidenceRepresentation maps one row to its E27 representation.
func vehiclePositionEvidenceRepresentation(row dbgen.GetVehiclePositionEvidenceRow, l itemLinks) vehiclePositionEvidence {
	ev := vehiclePositionEvidence{
		VehiclePositionEvidenceID: row.VehiclePositionEvidenceID,
		ObservedAt:                newAPITime(row.ObservedAt),
		Incrementality:            row.Incrementality,
		FeedEntityID:              row.FeedEntityID,
		AgencyID:                  row.AgencyID,
		AttributionReason:         row.AttributionReason,
		VehicleSourceTime:         newOptionalAPITime(row.VehicleSourceTime),
		Latitude:                  row.Latitude,
		Longitude:                 row.Longitude,
		PositionValid:             row.PositionValid,
		PositionValidationReason:  row.PositionValidationReason,
		ReportedSpeedMps:          row.ReportedSpeedMps,
		ComputedSpeedMps:          row.ComputedSpeedMps,
		SpeedDiscrepancyRatio:     row.SpeedDiscrepancyRatio,
		TripID:                    row.TripID,
		RouteID:                   row.RouteID,
		ScheduleRelationship:      row.ScheduleRelationship,
		ExpectedScheduleState:     row.ExpectedScheduleState,
		ExpectedScheduleReason:    row.ExpectedScheduleReason,
		ExpectedWindowStart:       newOptionalAPITime(row.ExpectedWindowStart),
		ExpectedWindowEnd:         newOptionalAPITime(row.ExpectedWindowEnd),
		Links:                     l.links(),
	}
	if row.CurrentStatus != nil {
		v := int64(*row.CurrentStatus)
		ev.CurrentStatus = &v
	}
	if row.TripStartTimeSeconds != nil {
		v := int64(*row.TripStartTimeSeconds)
		ev.TripStartTimeSeconds = &v
	}
	if row.TripStartDate.Valid {
		d := apiDate(row.TripStartDate.Time)
		ev.TripStartDate = &d
	}
	return ev
}

// getVehiclePositionEvidence serves GET /v1/vehicle-position-evidence/:evidence_id (E27).
func (s *InvestigationService) getVehiclePositionEvidence(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "evidence_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, vehiclePositionEvidenceIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehiclePositionEvidenceBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		rows, err := q.GetVehiclePositionEvidence(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return pgx.ErrNoRows
		}
		l := vehiclePositionEvidenceLinks(rows[0])
		body.vehiclePositionEvidence = vehiclePositionEvidenceRepresentation(rows[0], l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Evidence never changes once written, so the representation is immutable.
	// No Last-Modified: the record has no single time to offer. Any include gets
	// the short policy, as in E16, E18 and E19, so the rule does not depend on
	// which names this endpoint can include.
	cache := cacheImmutable(time.Time{})
	if inc.requested() {
		cache = cacheShort()
	}
	s.writeJSON(w, r, body, cache)
}

// loadVehiclePositionEvidence is the include loader for kindVehiclePositionEvidence.
// It returns each record's E27 representation, keyed by its ID, in one query.
func loadVehiclePositionEvidence(ctx context.Context, q *dbgen.Queries, ids []int64) (map[int64]any, error) {
	rows, err := q.GetVehiclePositionEvidence(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]any, len(rows))
	for _, row := range rows {
		out[row.VehiclePositionEvidenceID] = vehiclePositionEvidenceRepresentation(row, vehiclePositionEvidenceLinks(row))
	}
	return out, nil
}
