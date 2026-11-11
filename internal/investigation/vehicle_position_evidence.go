package investigation

import (
	"context"
	"net/http"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

// vehiclePositionEvidence is one evidence record in a response (E27).
type vehiclePositionEvidence struct {
	VehiclePositionEvidenceID int64    `json:"vehicle_position_evidence_id"`
	VehiclePositionResponseID int64    `json:"vehicle_position_response_id"`
	ObservedAt                apiTime  `json:"observed_at"`
	VehicleEntityID           *int64   `json:"vehicle_entity_id"`
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
	CurrentStatus             *int16   `json:"current_status"`
	TripID                    *string  `json:"trip_id"`
	RouteID                   *string  `json:"route_id"`
	TripStartDate             *apiDate `json:"trip_start_date"`
	TripStartTimeSeconds      *int32   `json:"trip_start_time_seconds"`
	ScheduleRelationship      *string  `json:"schedule_relationship"`
	ExpectedScheduleState     *string  `json:"expected_schedule_state"`
	ExpectedScheduleReason    *string  `json:"expected_schedule_reason"`
	ExpectedWindowStart       *apiTime `json:"expected_window_start"`
	ExpectedWindowEnd         *apiTime `json:"expected_window_end"`
	GtfsStaticVersionID       *int64   `json:"gtfs_static_version_id"`
	BoundingBoxVersionID      *int64   `json:"bounding_box_version_id"`
	Links                     links    `json:"links"`
}

// getVehiclePositionEvidence serves GET /v1/vehicle-position-evidence/:evidence_id:
// one evidence record with every stored value.
func (s *InvestigationService) getVehiclePositionEvidence(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "evidence_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	parseQuery(r, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var row dbgen.GetVehiclePositionEvidenceRow
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		row, err = q.GetVehiclePositionEvidence(ctx, id)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, vehiclePositionEvidenceRepresentation(row), cacheImmutable(row.ObservedAt))
}

// vehiclePositionEvidenceRepresentation maps one row to the E27 response body.
func vehiclePositionEvidenceRepresentation(row dbgen.GetVehiclePositionEvidenceRow) vehiclePositionEvidence {
	evidenceID := strconv.FormatInt(row.VehiclePositionEvidenceID, 10)
	responseID := strconv.FormatInt(row.VehiclePositionResponseID, 10)
	endpointID := strconv.FormatInt(row.EndpointID, 10)

	out := links{
		"self":     "/v1/vehicle-position-evidence/" + evidenceID,
		"response": "/v1/vehicle-position-responses/" + responseID,
		"endpoint": "/v1/vehicle-position-endpoints/" + endpointID,
	}
	if row.VehicleEntityID != nil {
		out["vehicle"] = "/v1/vehicles/" + strconv.FormatInt(*row.VehicleEntityID, 10)
	}
	if row.AgencyID != nil {
		out["agency"] = "/v1/agencies/" + strconv.FormatInt(*row.AgencyID, 10)
	}
	if row.GtfsStaticVersionID != nil {
		out["gtfs_static_version"] = "/v1/gtfs-static-versions/" + strconv.FormatInt(*row.GtfsStaticVersionID, 10)
	}
	if row.BoundingBoxVersionID != nil {
		out["bounding_box_version"] = "/v1/bounding-box-versions/" + strconv.FormatInt(*row.BoundingBoxVersionID, 10)
	}

	return vehiclePositionEvidence{
		VehiclePositionEvidenceID: row.VehiclePositionEvidenceID,
		VehiclePositionResponseID: row.VehiclePositionResponseID,
		ObservedAt:                newAPITime(row.ObservedAt),
		VehicleEntityID:           row.VehicleEntityID,
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
		CurrentStatus:             row.CurrentStatus,
		TripID:                    row.TripID,
		RouteID:                   row.RouteID,
		TripStartDate:             newAPIDate(row.TripStartDate),
		TripStartTimeSeconds:      row.TripStartTimeSeconds,
		ScheduleRelationship:      row.ScheduleRelationship,
		ExpectedScheduleState:     row.ExpectedScheduleState,
		ExpectedScheduleReason:    row.ExpectedScheduleReason,
		ExpectedWindowStart:       newOptionalAPITime(row.ExpectedWindowStart),
		ExpectedWindowEnd:         newOptionalAPITime(row.ExpectedWindowEnd),
		GtfsStaticVersionID:       row.GtfsStaticVersionID,
		BoundingBoxVersionID:      row.BoundingBoxVersionID,
		Links:                     out,
	}
}
