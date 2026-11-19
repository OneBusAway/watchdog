package investigation

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"watchdog.onebusaway.org/internal/db/dbgen"
)

const (
	kindVehicleAnomalyRuleVersion     resourceKind = "vehicle_anomaly_rule_version"
	kindVehiclePositionEvidence       resourceKind = "vehicle_position_evidence"
	kindVehicleAnomalyEpisodeEvidence resourceKind = "vehicle_anomaly_episode_evidence"
)

// vehicleAnomalyCodes are the seeded vehicle_anomaly_type codes, the values anomaly_type accepts.
var vehicleAnomalyCodes = []string{"stale_source_timestamp", "invalid_coordinate", "stopped_out_of_bounds"}

// vehicleAnomalyEpisodeIncludable uses "vehicle_response" for both responses, the kind the vehicle-position-response endpoint registers.
var vehicleAnomalyEpisodeIncludable = map[string]resourceKind{
	"vehicle":          "vehicle",
	"endpoint":         kindEndpoint,
	"rule_version":     kindVehicleAnomalyRuleVersion,
	"opening_response": "vehicle_response",
	"closing_response": "vehicle_response",
	"opening_evidence": kindVehiclePositionEvidence,
	"evidence":         kindVehicleAnomalyEpisodeEvidence,
}

// vehicleAnomalyEpisode is the representation of one vehicle anomaly episode (E25).
type vehicleAnomalyEpisode struct {
	VehicleAnomalyEpisodeID int64    `json:"vehicle_anomaly_episode_id"`
	Status                  string   `json:"status"`
	AnomalyType             string   `json:"anomaly_type"`
	VehicleID               string   `json:"vehicle_id"`
	OpenedAt                apiTime  `json:"opened_at"`
	ClosedAt                *apiTime `json:"closed_at"`
	DurationSeconds         *int64   `json:"duration_seconds"`
	CloseReason             *string  `json:"close_reason"`
	PreviousEpisodeCount    int64    `json:"previous_episode_count"`
	PreviousClosedAt        *apiTime `json:"previous_closed_at"`
	Links                   links    `json:"links"`
}

// vehicleAnomalyEpisodeBody is the E25 response.
type vehicleAnomalyEpisodeBody struct {
	vehicleAnomalyEpisode
	Included included `json:"included,omitzero"`
}

// vehicleAnomalyEpisodeLinks builds an episode's links; closing_response and opening_evidence only exist when their rows do.
func vehicleAnomalyEpisodeLinks(row dbgen.VehicleAnomalyEpisodeView, openingEvidenceID *int64) itemLinks {
	self := "/v1/vehicle-anomaly-episodes/" + strconv.FormatInt(row.VehicleAnomalyEpisodeID, 10)
	l := itemLinks{
		"self": {URL: self},
		"vehicle": {
			Kind: "vehicle",
			ID:   row.VehicleEntityID,
			URL:  "/v1/vehicles/" + strconv.FormatInt(row.VehicleEntityID, 10),
		},
		"endpoint": {
			Kind: kindEndpoint,
			ID:   row.EndpointID,
			URL:  "/v1/vehicle-position-endpoints/" + strconv.FormatInt(row.EndpointID, 10),
		},
		"rule_version": {
			Kind: kindVehicleAnomalyRuleVersion,
			ID:   row.RuleVersionID,
			URL:  "/v1/vehicle-anomaly-rule-versions/" + strconv.FormatInt(row.RuleVersionID, 10),
		},
		"opening_response": {
			Kind: "vehicle_response",
			ID:   row.OpeningResponseID,
			URL:  "/v1/vehicle-position-responses/" + strconv.FormatInt(row.OpeningResponseID, 10),
		},
		"evidence": {
			Kind: kindVehicleAnomalyEpisodeEvidence,
			ID:   row.VehicleAnomalyEpisodeID,
			URL:  self + "/evidence",
		},
	}
	if row.ClosingResponseID != nil {
		l["closing_response"] = related{
			Kind: "vehicle_response",
			ID:   *row.ClosingResponseID,
			URL:  "/v1/vehicle-position-responses/" + strconv.FormatInt(*row.ClosingResponseID, 10),
		}
	}
	if openingEvidenceID != nil {
		l["opening_evidence"] = related{
			Kind: kindVehiclePositionEvidence,
			ID:   *openingEvidenceID,
			URL:  "/v1/vehicle-position-evidence/" + strconv.FormatInt(*openingEvidenceID, 10),
		}
	}
	return l
}

// vehicleAnomalyEpisodeRepresentation maps one view row to its E25 representation.
func vehicleAnomalyEpisodeRepresentation(row dbgen.VehicleAnomalyEpisodeView, l itemLinks) vehicleAnomalyEpisode {
	ep := vehicleAnomalyEpisode{
		VehicleAnomalyEpisodeID: row.VehicleAnomalyEpisodeID,
		Status:                  "open",
		AnomalyType:             row.AnomalyCode,
		VehicleID:               row.VehicleID,
		OpenedAt:                newAPITime(row.OpenedAt),
		ClosedAt:                newOptionalAPITime(row.ClosedAt),
		CloseReason:             row.CloseReason,
		PreviousEpisodeCount:    row.PreviousEpisodeCount,
		PreviousClosedAt:        newOptionalAPITime(row.PreviousClosedAt),
		Links:                   l.links(),
	}
	if row.ClosingResponseID != nil {
		ep.Status = "closed"
	}
	if row.ClosedAt != nil {
		seconds := int64(row.ClosedAt.Sub(row.OpenedAt) / time.Second)
		ep.DurationSeconds = &seconds
	}
	return ep
}

// getVehicleAnomalyEpisode serves GET /v1/vehicle-anomaly-episodes/:episode_id.
func (s *InvestigationService) getVehicleAnomalyEpisode(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id, ok := pathID(ps, "episode_id")
	if !ok {
		writeNotFound(w)
		return
	}
	var bad invalidParams
	values := parseQuery(r, &bad, "include")
	inc := s.parseInclude(values, vehicleAnomalyEpisodeIncludable, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}

	var body vehicleAnomalyEpisodeBody
	var closedAt *time.Time
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		row, err := q.GetVehicleAnomalyEpisode(ctx, id)
		if err != nil {
			return err
		}
		closedAt = row.VehicleAnomalyEpisodeView.ClosedAt
		l := vehicleAnomalyEpisodeLinks(row.VehicleAnomalyEpisodeView, row.OpeningEvidenceID)
		body.vehicleAnomalyEpisode = vehicleAnomalyEpisodeRepresentation(row.VehicleAnomalyEpisodeView, l)
		body.Included, err = s.loadIncluded(ctx, q, []itemLinks{l}, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cache := cacheShort()
	if closedAt != nil && !inc.requested() {
		cache = cacheImmutable(*closedAt)
	}
	s.writeJSON(w, r, body, cache)
}
