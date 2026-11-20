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
var vehicleAnomalyCodes = []string{episodeTypeStaleSource, episodeTypeInvalidCoordinate, episodeTypeStoppedOutOfBounds}

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

// vehicleAnomalyEpisodesBody is the E23 and E24 response.
type vehicleAnomalyEpisodesBody struct {
	Items    []vehicleAnomalyEpisode `json:"items"`
	Links    links                   `json:"links"`
	Included included                `json:"included,omitzero"`
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

// vehicleAnomalyEpisodePage reads one page of ListVehicleAnomalyEpisodes for E24 and E23.
// The items are never nil, so an empty page encodes as [] rather than null.
func (s *InvestigationService) vehicleAnomalyEpisodePage(ctx context.Context, q *dbgen.Queries, r *http.Request,
	params dbgen.ListVehicleAnomalyEpisodesParams, p page, inc include) (vehicleAnomalyEpisodesBody, error) {
	var body vehicleAnomalyEpisodesBody
	rows, err := q.ListVehicleAnomalyEpisodes(ctx, params)
	if err != nil {
		return body, err
	}
	rows, hasMore := trimPage(rows, p)
	body.Items = make([]vehicleAnomalyEpisode, 0, len(rows))
	all := make([]itemLinks, 0, len(rows))
	for _, row := range rows {
		l := vehicleAnomalyEpisodeLinks(row.VehicleAnomalyEpisodeView, row.OpeningEvidenceID)
		all = append(all, l)
		body.Items = append(body.Items, vehicleAnomalyEpisodeRepresentation(row.VehicleAnomalyEpisodeView, l))
	}
	var lastID int64
	if len(rows) > 0 {
		lastID = rows[len(rows)-1].VehicleAnomalyEpisodeView.VehicleAnomalyEpisodeID
	}
	body.Links = pageLinks(r, lastID, hasMore)
	body.Included, err = s.loadIncluded(ctx, q, all, inc)
	return body, err
}

var vehiclePositionValidationReasons = []string{
	"missing_position",
	"non_finite_coordinate",
	"placeholder_zero_coordinate",
	"latitude_out_of_range",
	"longitude_out_of_range",
}

var vehicleAttributionReasons = []string{
	"missing_vehicle_id",
	"missing_route_id",
	"unknown_route_id",
	"agency_not_reported_by_oba",
	"ambiguous_identifier",
	"route_trip_conflict",
}

var vehicleExpectedScheduleStates = []string{
	"expected_active",
	"expected_inactive",
	"unknown",
}

// listVehicleAnomalyEpisodes serves GET /v1/vehicle-anomaly-episodes.
func (s *InvestigationService) listVehicleAnomalyEpisodes(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var bad invalidParams
	values := parseQuery(r, &bad, "server_id", "agency_id", "endpoint_id", "vehicle_id", "anomaly_type", "status",
		"active_from", "active_to", "opened_from", "opened_to", "open_at", "opening_response_id", "closing_response_id",
		"gtfs_static_version_id", "recurrence", "route_id", "rule_version_id", "bounding_box_version_id",
		"position_validation_reason", "attribution_reason", "expected_schedule_state",
		"close_reason", "include", "page_size", "cursor")

	var agencyID *int64
	var agencyNone *bool
	if values.Has("agency_id") {
		raw := values.Get("agency_id")
		if raw == "none" {
			t := true
			agencyNone = &t
		} else {
			agencyID = paramID(values, "agency_id", &bad)
		}
	}

	params := dbgen.ListVehicleAnomalyEpisodesParams{
		ServerID:                 paramID(values, "server_id", &bad),
		AgencyID:                 agencyID,
		AgencyNone:               agencyNone,
		EndpointID:               paramID(values, "endpoint_id", &bad),
		ExternalVehicleID:        paramString(values, "vehicle_id", &bad),
		AnomalyCode:              paramEnum(values, "anomaly_type", vehicleAnomalyCodes, &bad),
		Status:                   paramEnum(values, "status", []string{"open", "closed"}, &bad),
		CloseReason:              paramEnum(values, "close_reason", vehicleCloseReasons, &bad),
		OpeningResponseID:        paramID(values, "opening_response_id", &bad),
		ClosingResponseID:        paramID(values, "closing_response_id", &bad),
		GtfsStaticVersionID:      paramID(values, "gtfs_static_version_id", &bad),
		Recurrence:               paramEnum(values, "recurrence", []string{"has_previous_episode", "no_previous_episode"}, &bad),
		RouteID:                  paramString(values, "route_id", &bad),
		RuleVersionID:            paramID(values, "rule_version_id", &bad),
		BoundingBoxVersionID:     paramID(values, "bounding_box_version_id", &bad),
		PositionValidationReason: paramEnum(values, "position_validation_reason", vehiclePositionValidationReasons, &bad),
		AttributionReason:        paramEnum(values, "attribution_reason", vehicleAttributionReasons, &bad),
		ExpectedScheduleState:    paramEnum(values, "expected_schedule_state", vehicleExpectedScheduleStates, &bad),
	}
	// A feed vehicle ID is only unique within one endpoint, so it needs a scope.
	// An empty vehicle_id is already reported by paramString, so it is not
	// reported a second time here.
	if params.ExternalVehicleID != nil && !values.Has("endpoint_id") && !values.Has("server_id") {
		bad.add("vehicle_id", "requires endpoint_id or server_id")
	}
	hasOpenedWindow := values.Has("opened_from") || values.Has("opened_to")
	hasOpenAt := values.Has("open_at")
	replaced := hasOpenedWindow || hasOpenAt
	statusOpen := params.Status != nil && *params.Status == "open"
	win := s.parseWindow(values, activeWindowOptions(statusOpen, replaced), &bad)
	openedWin := s.parseWindow(values, openedWindowOptions(), &bad)
	openAt := paramTime(values, "open_at", &bad)
	notFuture("open_at", openAt, s.now(), &bad)
	inc := s.parseInclude(values, vehicleAnomalyEpisodeIncludable, &bad)
	p := parsePage(values, &bad)
	if len(bad) > 0 {
		writeInvalidParams(w, bad)
		return
	}
	params.ActiveFrom, params.ActiveTo = win.From, win.To
	params.OpenedFrom, params.OpenedTo = openedWin.From, openedWin.To
	params.OpenAt = openAt
	params.AfterID, params.PageLimit = p.AfterID, p.Limit()

	var body vehicleAnomalyEpisodesBody
	err := s.read(r.Context(), func(ctx context.Context, q *dbgen.Queries) error {
		var err error
		body, err = s.vehicleAnomalyEpisodePage(ctx, q, r, params, p, inc)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, body, cacheShort())
}
